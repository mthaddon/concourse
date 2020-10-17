package watch

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"

	"code.cloudfoundry.org/lager"
	sq "github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/lock"
)

type JobSummaryEvent struct {
	ID   int
	Type EventType
	Job  *atc.JobSummary
}

type ListAllJobsWatcher struct {
	logger      lager.Logger
	conn        db.Conn
	lockFactory lock.LockFactory

	mtx         sync.RWMutex
	subscribers map[chan []JobSummaryEvent]struct{}
}

var listAllJobsWatchTables = []watchTable{
	{
		table: "jobs",
		idCol: "id",

		insert: true,
		update: true,
		updateCols: []string{
			"name", "active", "paused", "has_new_inputs", "tags", "latest_completed_build_id",
			"next_build_id", "transition_build_id", "config",
		},
		delete: true,
	},
	{
		table: "pipelines",
		idCol: "id",

		update:     true,
		updateCols: []string{"name", "public"},
	},
	{
		table: "teams",
		idCol: "id",

		update:     true,
		updateCols: []string{"name"},
	},
}

func NewListAllJobsWatcher(logger lager.Logger, conn db.Conn, lockFactory lock.LockFactory) (*ListAllJobsWatcher, error) {
	watcher := &ListAllJobsWatcher{
		logger:      logger,
		conn:        conn,
		lockFactory: lockFactory,

		subscribers: make(map[chan []JobSummaryEvent]struct{}),
	}

	if err := watcher.setupTriggers(); err != nil {
		return nil, fmt.Errorf("setup triggers: %w", err)
	}

	notifs, err := watcher.conn.Bus().Listen(eventsChannel, db.QueueNotifications)
	if err != nil {
		return nil, fmt.Errorf("listen: %w", err)
	}

	go watcher.drain(notifs)

	return watcher, nil
}

func (w *ListAllJobsWatcher) setupTriggers() error {
	l, acquired, err := w.lockFactory.Acquire(w.logger, lock.NewCreateWatchTriggersLockID())
	if err != nil {
		return fmt.Errorf("acquire lock: %w", err)
	}
	if !acquired {
		w.logger.Debug("lock-already-held")
		return nil
	}
	defer l.Release()

	tx, err := w.conn.Begin()
	if err != nil {
		return err
	}

	defer tx.Rollback()

	if _, err = tx.Exec(createNotifyTriggerFunction); err != nil {
		return fmt.Errorf("create notify function: %w", err)
	}

	for _, tbl := range listAllJobsWatchTables {
		if err = createWatchEventsTrigger(tx, tbl); err != nil {
			return fmt.Errorf("create trigger for %s: %w", tbl.table, err)
		}
	}

	err = tx.Commit()
	if err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

func (w *ListAllJobsWatcher) WatchListAllJobs(ctx context.Context) (<-chan []JobSummaryEvent, error) {
	eventsChan := make(chan []JobSummaryEvent)

	dirty := make(chan struct{})
	var pendingEvents []JobSummaryEvent
	var mtx sync.Mutex
	go w.watchEvents(ctx, &pendingEvents, &mtx, dirty)
	go w.sendEvents(ctx, eventsChan, &pendingEvents, &mtx, dirty)
	return eventsChan, nil
}

func (w *ListAllJobsWatcher) watchEvents(
	ctx context.Context,
	pendingEvents *[]JobSummaryEvent,
	mtx *sync.Mutex,
	dirty chan<- struct{},
) {
	c := w.subscribe()
	defer w.unsubscribe(c)
	for {
		select {
		case <-ctx.Done():
			return
		case evts, ok := <-c:
			if !ok {
				return
			}
			mtx.Lock()
			*pendingEvents = append(*pendingEvents, evts...)
			if len(*pendingEvents) > 0 {
				invalidate(dirty)
			}
			mtx.Unlock()
		}
	}
}

func (w *ListAllJobsWatcher) sendEvents(
	ctx context.Context,
	eventsChan chan<- []JobSummaryEvent,
	pendingEvents *[]JobSummaryEvent,
	mtx *sync.Mutex,
	dirty <-chan struct{},
) {
	defer close(eventsChan)
	for {
		select {
		case <-ctx.Done():
			return
		case <-dirty:
		}
		mtx.Lock()
		eventsToSend := make([]JobSummaryEvent, len(*pendingEvents))
		copy(eventsToSend, *pendingEvents)
		*pendingEvents = (*pendingEvents)[:0]
		mtx.Unlock()

		select {
		case eventsChan <- eventsToSend:
		case <-ctx.Done():
			return
		}
	}
}

func invalidate(dirty chan<- struct{}) {
	select {
	case dirty <- struct{}{}:
	default:
	}
}

func (w *ListAllJobsWatcher) subscribe() chan []JobSummaryEvent {
	c := make(chan []JobSummaryEvent)

	w.mtx.Lock()
	defer w.mtx.Unlock()
	w.subscribers[c] = struct{}{}

	return c
}

func (w *ListAllJobsWatcher) unsubscribe(c chan []JobSummaryEvent) {
	w.mtx.Lock()
	defer w.mtx.Unlock()
	delete(w.subscribers, c)
}

func (w *ListAllJobsWatcher) noSubscribers() bool {
	w.mtx.RLock()
	defer w.mtx.RUnlock()
	return len(w.subscribers) == 0
}

func (w *ListAllJobsWatcher) terminateSubscribers() {
	w.mtx.Lock()
	defer w.mtx.Unlock()
	for c := range w.subscribers {
		close(c)
		delete(w.subscribers, c)
	}
}

func (w *ListAllJobsWatcher) drain(notifs chan db.Notification) {
	for notif := range notifs {
		if notif.Healthy {
			if err := w.process(notif.Payload); err != nil {
				w.logger.Error("process-notification", err, lager.Data{"payload": notif.Payload})
			}
		} else {
			w.terminateSubscribers()
		}
	}
}

func (w *ListAllJobsWatcher) process(payload string) error {
	if w.noSubscribers() {
		return nil
	}
	var notif Notification
	err := json.Unmarshal([]byte(payload), &notif)
	if err != nil {
		return err
	}
	var pred interface{}
	var jobID int
	switch notif.Table {
	case "jobs":
		jobID, pred, err = intEqPred("j.id", notif.Data["id"])
		if notif.Operation == "DELETE" {
			w.publishEvents(JobSummaryEvent{
				ID:   jobID,
				Type: Delete,
			})
			return nil
		}
	case "pipelines":
		_, pred, err = intEqPred("p.id", notif.Data["id"])
	case "teams":
		_, pred, err = intEqPred("tm.id", notif.Data["id"])
	default:
		return nil
	}
	if err != nil {
		return err
	}
	jobs, err := w.fetchJobs(pred)
	if err != nil {
		return err
	}
	if len(jobs) == 0 {
		// an update to a job that results in it not being found is updating active to false (or it was already false).
		// either way, sending a 'DELETE' is reasonable, as long as we make no guarantees about repeated DELETEs
		if notif.Table == "jobs" && notif.Operation == "UPDATE" {
			w.publishEvents(JobSummaryEvent{
				ID:   jobID,
				Type: Delete,
			})
		}
		return nil
	}
	evts := make([]JobSummaryEvent, len(jobs))
	for i, job := range jobs {
		evts[i] = JobSummaryEvent{
			ID:   job.ID,
			Type: Put,
			Job:  &jobs[i],
		}
	}
	w.publishEvents(evts...)
	return nil
}

func (w *ListAllJobsWatcher) fetchJobs(pred interface{}) ([]atc.JobSummary, error) {
	tx, err := w.conn.Begin()
	if err != nil {
		return nil, err
	}

	defer tx.Rollback()

	factory := db.NewDashboardFactory(tx, pred)
	dashboard, err := factory.BuildDashboard()
	if err != nil {
		return nil, err
	}
	err = tx.Commit()
	if err != nil {
		return nil, err
	}
	return dashboard, nil
}

func (w *ListAllJobsWatcher) publishEvents(evts ...JobSummaryEvent) {
	w.mtx.RLock()
	defer w.mtx.RUnlock()
	for c := range w.subscribers {
		c <- evts
	}
}

func intEqPred(col string, raw string) (int, interface{}, error) {
	val, err := strconv.Atoi(raw)
	if err != nil {
		return 0, nil, err
	}
	return val, sq.Eq{col: val}, nil
}
