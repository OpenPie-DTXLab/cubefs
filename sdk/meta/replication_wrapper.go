package meta

import (
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/cubefs/cubefs/proto"
)

type ReplicationWrapper struct {
	Client       *s3.S3
	TargetConfig proto.ReplicationTarget
}

func updateTarget(wrapper *ReplicationWrapper, target proto.ReplicationTarget) {
	wrapper.TargetConfig.ReplicationSync = target.ReplicationSync
	//todo
}
