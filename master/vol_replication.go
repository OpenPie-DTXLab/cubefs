package master

import (
	"net/url"
)

// StatusType of Replication for x-amz-replication-status header
type StatusType string

const (
	// Pending - replication is pending.
	Pending StatusType = "PENDING"

	// Complete - replication completed ok.
	Complete StatusType = "COMPLETE"

	// Failed - replication failed.
	Failed StatusType = "FAILED"

	// Replica - this is a replica.
	Replica StatusType = "REPLICA"
)

// String returns string representation of status
func (s StatusType) String() string {
	return string(s)
}

type ReplicationTarget struct {
	ID              string `json:"id,omitempty"`
	SourceVolume    string `json:"sourceVolume"`
	Endpoint        string `json:"endpoint"`
	AccessKey       string `json:"accessKey"`
	SecretKey       string `json:"SecretKey"`
	TargetVolume    string `json:"targetVolume"`
	Region          string `json:"region,omitempty"`
	Secure          bool   `json:"secure"`
	ReplicationSync bool   `json:"replicationSync"`
}

func (t ReplicationTarget) URL() *url.URL {
	scheme := "http"
	if t.Secure {
		scheme = "https"
	}
	return &url.URL{
		Scheme: scheme,
		Host:   t.Endpoint,
	}
}

// Config - replication configuration
//type Config struct {
//	Rules   []ReplicationRule   `json:"ReplicationRules"`
//	// Arn identify the target bucket uniquely
//	Arn string `json:"Arn,omitempty"`
//}
