package vol_replication

// ReplicationStatusType of Replication for x-amz-replication-status header
type ReplicationStatusType string

const (
	// Pending - replication is pending.
	Pending ReplicationStatusType = "PENDING"

	// Complete - replication completed ok.
	Complete ReplicationStatusType = "COMPLETE"

	// Failed - replication failed.
	Failed ReplicationStatusType = "FAILED"

	// Replica - this is a replica.
	Replica ReplicationStatusType = "REPLICA"
)

// String returns string representation of status
func (s ReplicationStatusType) String() string {
	return string(s)
}
