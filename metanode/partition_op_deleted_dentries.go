package metanode

import (
	"encoding/json"
	"github.com/cubefs/cubefs/proto"
	"sort"
)

func (mp *metaPartition) AppendDeletedDentry(req *proto.AppendDeletedEntryRequest, p *Packet) (err error) {
	deletedDentry := &proto.DeletedDentryInfo{
		PartitionID: req.PartitionID,
		Inode:       req.Inode,
		Path:        req.Path,
		Time:        req.Time,
	}
	val, err := json.Marshal(&deletedDentry)
	if err != nil {
		return
	}
	resp, err := mp.submit(opFSMAppendDeletedDentry, val)
	if err != nil {
		p.PacketErrorWithBody(proto.OpAgain, []byte(err.Error()))
		return
	}
	p.ResultCode = resp.(uint8)
	return

}

func (mp *metaPartition) RemoveDeletedDentry(req *proto.RemoveDeletedEntryRequest, p *Packet) (err error) {
	deletedDentry := &proto.DeletedDentryInfo{
		PartitionID: req.PartitionID,
		Path:        req.Path,
	}
	val, err := json.Marshal(&deletedDentry)
	if err != nil {
		return
	}
	resp, err := mp.submit(opFSMRemoveDeletedDentry, val)
	if err != nil {
		p.PacketErrorWithBody(proto.OpAgain, []byte(err.Error()))
		return
	}
	p.ResultCode = resp.(uint8)
	return

}

func (mp *metaPartition) ListDeletedDentries(p *Packet) (err error) {
	resp := proto.ListDeletedEntryResponse{}
	mp.deletedDentriesLock.RLock()
	defer mp.deletedDentriesLock.RUnlock()

	for _, dentry := range mp.deletedDentries {
		resp.DeletedDentries = append(resp.DeletedDentries, dentry)
	}

	reply, err := json.Marshal(resp)
	if err != nil {
		p.PacketErrorWithBody(proto.OpErr, []byte(err.Error()))
		return
	}
	p.PacketOkWithBody(reply)
	return

}

func (mp *metaPartition) GetDeletedDentries() (infoList []*proto.DeletedDentryInfo) {
	mp.deletedDentriesLock.RLock()
	for _, v := range mp.deletedDentries {
		infoList = append(infoList, v)
	}

	mp.deletedDentriesLock.RUnlock()

	sort.SliceStable(infoList, func(i, j int) bool {
		return infoList[i].Inode > infoList[j].Inode
	})
	return
}
