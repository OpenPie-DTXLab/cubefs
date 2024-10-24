package proto

import (
	"net/url"
)

type ScanStatistics struct {
	TraverseDone     bool
	TraverseStatus   string
	Done             bool
	FileHandleStatus string

	TotalInodeScannedNum  int64
	FileScannedNum        int64
	DirScannedNum         int64
	ErrorSkippedNum       int64
	FailedObjectsDetected int64
	FailedObjectsHealed   int64

	FailedDeletion        int64
	FailedDeletionHealed  int64
	FailedDeletionSkipped int64
}

type ReplicationTarget struct {
	ID              string `json:"id,omitempty"`
	SourceVolume    string `json:"sourceVolume"`
	Endpoint        string `json:"endpoint"`
	AccessKey       string `json:"accessKey"`
	SecretKey       string `json:"SecretKey"`
	TargetVolume    string `json:"targetVolume"`
	Prefix          string `json:"Prefix"`
	Region          string `json:"region,omitempty"`
	Secure          bool   `json:"secure"`
	ReplicationSync bool   `json:"replicationSync"`
	Status          bool   `json:"status"`
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
