package forward

import "github.com/rhisijmuiden/unify-syslog-server/internal/diskqueue"

// The per-destination queue lives in package diskqueue, shared with probes.
type (
	Queue = diskqueue.Queue
	Item  = diskqueue.Item
)

var OpenQueue = diskqueue.Open
