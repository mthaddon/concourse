def Settings( **kwargs ):
  if kwargs[ 'language' ] == 'go':
    return {
      'ls': {
        'buildFlags': ['-tags', 'linux']
      }
    }
