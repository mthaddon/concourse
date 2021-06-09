package worker_test

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/integration/internal/dctest"
	"github.com/concourse/concourse/integration/internal/flytest"
	"github.com/stretchr/testify/require"
)

func TestRestart_AttachToRunningBuild(t *testing.T) {
	t.Parallel()

	dc := dctest.Init(t, "../docker-compose.yml", "overrides/restartable.yml")

	t.Run("deploy", func(t *testing.T) {
		dc.Run(t, "up", "-d")
	})

	fly := flytest.Init(t, dc)
	buf := new(bytes.Buffer)
	executeCmd := fly.OutputTo(buf).Start(t, "execute", "-c", "tasks/wait.yml")
	require.Eventually(t, func() bool {
		return strings.Contains(buf.String(), "waiting for /tmp/stop-waiting to exist")
	}, 1*time.Minute, 1*time.Second)

	buildRegex := regexp.MustCompile(`executing build (\d+)`)
	matches := buildRegex.FindSubmatch(buf.Bytes())
	buildID := string(matches[1])

	t.Run("restart worker process", func(t *testing.T) {
		// entrypoint script traps SIGHUP and restarts the process
		dc.Run(t, "kill", "-s", "SIGHUP", "worker")

		workerReady := func() bool {
			err := dc.Try("exec", "-T", "worker", "stat", "/ready")
			return err == nil
		}

		require.Eventually(t, workerReady, 1*time.Minute, 1*time.Second)
	})

	fly.Run(t, "hijack", "-b", buildID, "-s", "one-off", "--", "touch", "/tmp/stop-waiting")

	// assert exits successfully
	err := executeCmd.Wait()
	require.NoError(t, err)

	require.True(t, strings.Contains(buf.String(), "done"))
}
