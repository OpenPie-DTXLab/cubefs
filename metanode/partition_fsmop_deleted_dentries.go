package metanode

import "github.com/cubefs/cubefs/proto"

func (mp *metaPartition) fsmAppendDeletedDentry(dentry *proto.DeletedDentryInfo) (status uint8) {

	var exist bool
	var oldDeletedDentry *proto.DeletedDentryInfo
	mp.deletedDentriesLock.Lock()
	defer mp.deletedDentriesLock.Unlock()

	if oldDeletedDentry, exist = mp.deletedDentries[dentry.Path]; !exist {
		mp.deletedDentries[dentry.Path] = dentry
		return proto.OpOk
	}

	if oldDeletedDentry.Time < dentry.Time {
		mp.deletedDentries[dentry.Path] = dentry
	}

	return proto.OpOk
}

func (mp *metaPartition) fsmRemoveDeletedDentry(dentry *proto.DeletedDentryInfo) (status uint8) {
	mp.deletedDentriesLock.Lock()
	defer mp.deletedDentriesLock.Unlock()

	delete(mp.deletedDentries, dentry.Path)
	return proto.OpOk
}
