package meta

import (
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/cubefs/cubefs/master"
)

type ReplicationWrapper struct {
	Client       *s3.S3
	TargetConfig master.ReplicationTarget
}

func updateTarget(wrapper *ReplicationWrapper, target master.ReplicationTarget) {
	wrapper.TargetConfig.ReplicationSync = target.ReplicationSync
	//todo
}
