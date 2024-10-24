package objectnode

import (
	"github.com/cubefs/cubefs/util/log"
	"runtime"
)

type ReplicateFileInfo struct {
	Inode      uint64
	Path       string
	CreateTime int64
	Size       uint64
	TargetIds  []string
}

type DeletionInfo struct {
	ParentIno uint64
	Inode     uint64
	Path      string
	Time      int64
	TargetIds []string
}

type ReplicationState struct {
	replicationCh chan ReplicateFileInfo
	deletionCh    chan DeletionInfo
}

func (r *ReplicationState) queueReplicaTask(info ReplicateFileInfo) {
	if r == nil {
		return
	}
	select {
	case r.replicationCh <- info:
	default:
		log.LogErrorf("discard file (inode:%v, path:%v) when async replication, because the replication chan is full ",
			info.Inode, info.Path)
	}
}

func (r *ReplicationState) queueDeletionTask(info DeletionInfo) {
	if r == nil {
		return
	}
	select {
	case r.deletionCh <- info:
	default:
		log.LogErrorf("discard deletion (inode:%v, path:%v) when async deletion, because the deletion chan is full ",
			info.Inode, info.Path)
	}
}

func NewReplicationState(closeCh chan struct{}, volume *Volume) *ReplicationState {
	rs := &ReplicationState{
		replicationCh: make(chan ReplicateFileInfo, 100000),
		deletionCh:    make(chan DeletionInfo, 100000),
	}

	// add background groutines to deal with replication tasks
	workerNum := runtime.GOMAXPROCS(0) / 2
	if workerNum == 0 {
		workerNum = 1
	}
	for i := 0; i < workerNum; i++ {
		go func() {
			for {
				select {
				case <-closeCh:
					return
				case info, ok := <-rs.replicationCh:
					if !ok {
						// chan closed
						return
					}
					if attrInfo, err := volume.mw.XAttrGetAll_ll(info.Inode); err == nil {
						volume.replicateObject(info.Path, info.Inode, info.Size, info.CreateTime, attrInfo.XAttrs, info.TargetIds)
					} else {
						log.LogErrorf("err when asynchronous replicate in background groutine: volume(%v) path(%v) inode(%v) err(%v)",
							volume.name, info.Path, info.Inode, err)
					}
				}
			}
		}()
	}

	for i := 0; i < workerNum; i++ {
		go func() {
			for {
				select {
				case <-closeCh:
					return
				case deletionInfo, ok := <-rs.deletionCh:
					if !ok {
						// chan closed
						return
					}
					ReplicateDeletion(volume.mw, deletionInfo.Inode, volume.name, deletionInfo.Path, deletionInfo.TargetIds, deletionInfo.Time)
				}
			}
		}()
	}

	// cleanup when close
	go func() {
		select {
		case <-closeCh:
		}

		close(rs.replicationCh)
	}()

	return rs
}
