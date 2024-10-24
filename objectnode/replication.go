package objectnode

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/cubefs/cubefs/sdk/meta"
	"github.com/cubefs/cubefs/sdk/meta/vol_replication"
	"github.com/cubefs/cubefs/util/log"
	"io"
	"math/rand"
	"strconv"
	"strings"
	"sync"
	"time"
)

func ReplicateObject(volName string, path string, size uint64, etagStr string, w *meta.ReplicationWrapper, metaData map[string]string, reader *io.PipeReader) (err error) {
	var out *s3.PutObjectOutput
	var input *s3.PutObjectInput
	var md5SumIsMatch bool

	if input, err = buildPutObjectInput(path, size, etagStr, w, metaData, reader); err != nil {
		log.LogErrorf("buildPutObjectInputMetadata: object: %v/%v failed to replicate to %v , err: %v", volName, path, w.TargetConfig.ID, err)
		return
	}
	out, err = w.Client.PutObject(input)
	if err != nil {
		log.LogErrorf("PutObject: object: %v/%v failed to replicate to %v , err: %v", volName, path, w.TargetConfig.ID, err)
		return
	}

	md5SumIsMatch = *out.ETag == wrapUnescapedQuot(etagStr)
	if !md5SumIsMatch {
		return fmt.Errorf("object: %v/%v failed to replicate to %v , md5sum mismatch ! ", volName, path, w.TargetConfig.ID)
	}

	return nil
}

func buildPutObjectInput(path string, size uint64, etagStr string, w *meta.ReplicationWrapper, metaData map[string]string, reader *io.PipeReader) (input *s3.PutObjectInput, err error) {
	var (
		attrsCopy map[string]string
		etagBytes []byte
		etag      ETagValue
	)

	attrsCopy = make(map[string]string, len(metaData))
	for k, v := range metaData {
		if k == XAttrKeyOSSETag {
			continue
		}
		attrsCopy[k] = v
	}

	input = &s3.PutObjectInput{
		Bucket:        aws.String(w.TargetConfig.TargetVolume),
		Key:           aws.String(path),
		Body:          aws.ReadSeekCloser(reader),
		ContentLength: aws.Int64(int64(size)),
		Metadata:      map[string]*string{},
	}

	// header
	etag = ParseETagValue(etagStr)
	if etagBytes, err = hex.DecodeString(etag.Value); err != nil {
		return nil, err
	}
	input.ContentMD5 = aws.String(base64.StdEncoding.EncodeToString(etagBytes))

	if _, exist := attrsCopy[XAttrKeyOSSCacheControl]; exist {
		input.CacheControl = aws.String(attrsCopy[XAttrKeyOSSCacheControl])
		delete(attrsCopy, XAttrKeyOSSCacheControl)
	}

	if _, exist := attrsCopy[XAttrKeyOSSMIME]; exist {
		input.ContentType = aws.String(attrsCopy[XAttrKeyOSSMIME])
		delete(attrsCopy, XAttrKeyOSSMIME)
	}

	if _, exist := attrsCopy[XAttrKeyOSSDISPOSITION]; exist {
		input.ContentDisposition = aws.String(attrsCopy[XAttrKeyOSSDISPOSITION])
		delete(attrsCopy, XAttrKeyOSSDISPOSITION)
	}

	if _, exist := attrsCopy[XAttrKeyOSSTagging]; exist {
		input.Tagging = aws.String(attrsCopy[XAttrKeyOSSTagging])
		delete(attrsCopy, XAttrKeyOSSTagging)
	}

	if _, exist := attrsCopy[XAttrKeyOSSExpires]; exist {
		var t time.Time
		if t, err = time.Parse(RFC1123Format, attrsCopy[XAttrKeyOSSExpires]); err != nil {
			log.LogErrorf("invalid expires format when replicate object [%v] , err: %v", path, err)
			return nil, err
		}
		input.Expires = aws.Time(t)
		delete(attrsCopy, XAttrKeyOSSExpires)
	}
	// TODO  how to parse ACL

	// metadata
	for k, v := range attrsCopy {
		// the ReplicationStatus of replicated objects is always Replica
		if k == VolumeReplicationStatus {
			input.Metadata[k] = aws.String(vol_replication.Replica.String())
			continue
		}

		input.Metadata[k] = aws.String(v)
	}

	return input, nil
}

func ReplicateMultiPartsObject(volName string, path string, size uint64, w *meta.ReplicationWrapper, metaData map[string]string, reader *io.PipeReader) (err error) {
	var (
		sizes     []uint64
		totalSize uint64
		s         uint64

		input          *s3.CreateMultipartUploadInput
		resp           *s3.CreateMultipartUploadOutput
		bufferReader   io.Reader
		partUploadResp *s3.UploadPartOutput
		completedParts []*s3.CompletedPart
	)

	if _, exist := metaData[XAttrKeyOSSPartSizes]; !exist {
		return fmt.Errorf("replicateMultiPartsObject: object %v/%v  part sizes not found ! ", volName, path)
	}
	partSizes := metaData[XAttrKeyOSSPartSizes]
	sizesStr := strings.Split(partSizes, ",")

	for _, str := range sizesStr {
		if s, err = strconv.ParseUint(str, 10, 64); err != nil {
			return err
		}
		sizes = append(sizes, s)
		totalSize += s
	}
	if totalSize != size {
		return fmt.Errorf("object %v/%v bad XAttrKeyOSSPartSizes, total size %v, actually is %v", volName, path, totalSize, size)

	}

	if input, err = buildMultipartUploadInput(path, w, metaData); err != nil {
		return
	}
	attempts := 1
	for attempts <= 3 {
		resp, err = w.Client.CreateMultipartUpload(input)
		if err == nil {
			break
		}
		log.LogErrorf("create multipart upload err when replicate object [%v] , err: %v", path, err)
		attempts++
		time.Sleep(time.Duration(rand.Int63n(int64(time.Second))))
	}

	if err != nil {
		return
	}

	defer func() {
		if err != nil {
			abortAttempts := 1
			for abortAttempts <= 3 {
				abortInput := &s3.AbortMultipartUploadInput{
					Bucket:   resp.Bucket,
					Key:      resp.Key,
					UploadId: resp.UploadId,
				}
				_, abortErr := w.Client.AbortMultipartUpload(abortInput)
				if abortErr == nil {
					return
				}
				abortAttempts++
				time.Sleep(time.Duration(rand.Int63n(int64(time.Second))))
			}
		}
	}()

	partNumber := 1
	for _, partSize := range sizes {

		bufferReader, err = adaptReader(reader, partSize)
		if err != nil {
			return
		}
		partInput := &s3.UploadPartInput{
			Body:          aws.ReadSeekCloser(bufferReader),
			Bucket:        resp.Bucket,
			Key:           resp.Key,
			PartNumber:    aws.Int64(int64(partNumber)),
			UploadId:      resp.UploadId,
			ContentLength: aws.Int64(int64(partSize)),
			// todo add checksum verification
			// ContentMD5: xxxx
		}

		partUploadResp, err = w.Client.UploadPart(partInput)
		if err != nil {
			return
		}

		completedParts = append(completedParts, &s3.CompletedPart{
			ETag:       partUploadResp.ETag,
			PartNumber: aws.Int64(int64(partNumber)),
		})
		partNumber++
	}

	completeInput := &s3.CompleteMultipartUploadInput{
		Bucket:   resp.Bucket,
		Key:      resp.Key,
		UploadId: resp.UploadId,
		MultipartUpload: &s3.CompletedMultipartUpload{
			Parts: completedParts,
		},
	}

	if _, err = w.Client.CompleteMultipartUpload(completeInput); err != nil {
		return
	}

	return
}

func adaptReader(reader *io.PipeReader, size uint64) (io.Reader, error) {
	var waitGroup sync.WaitGroup

	buffer := make([]byte, size)
	c := 0
	waitGroup.Add(1)
	go func() {
		for c < int(size) {
			n, err := reader.Read(buffer[c:])
			if err != nil {
				return
			}
			c += n
		}

		waitGroup.Done()
	}()

	waitGroup.Wait()

	return bytes.NewReader(buffer), nil
}

func buildMultipartUploadInput(path string, w *meta.ReplicationWrapper, metaData map[string]string) (input *s3.CreateMultipartUploadInput, err error) {
	input = &s3.CreateMultipartUploadInput{
		Bucket:   aws.String(w.TargetConfig.TargetVolume),
		Key:      aws.String(path),
		Metadata: map[string]*string{},
	}

	attrsCopy := make(map[string]string, len(metaData))
	for k, v := range metaData {
		if k == XAttrKeyOSSETag || k == XAttrKeyOSSPartSizes {
			continue
		}
		attrsCopy[k] = v
	}

	// header
	if _, exist := attrsCopy[XAttrKeyOSSCacheControl]; exist {
		input.CacheControl = aws.String(attrsCopy[XAttrKeyOSSCacheControl])
		delete(attrsCopy, XAttrKeyOSSCacheControl)
	}

	if _, exist := attrsCopy[XAttrKeyOSSMIME]; exist {
		input.ContentType = aws.String(attrsCopy[XAttrKeyOSSMIME])
		delete(attrsCopy, XAttrKeyOSSMIME)
	}

	if _, exist := attrsCopy[XAttrKeyOSSDISPOSITION]; exist {
		input.ContentDisposition = aws.String(attrsCopy[XAttrKeyOSSDISPOSITION])
		delete(attrsCopy, XAttrKeyOSSDISPOSITION)
	}

	if _, exist := attrsCopy[XAttrKeyOSSTagging]; exist {
		input.Tagging = aws.String(attrsCopy[XAttrKeyOSSTagging])
		delete(attrsCopy, XAttrKeyOSSTagging)
	}

	if _, exist := attrsCopy[XAttrKeyOSSExpires]; exist {
		var t time.Time
		if t, err = time.Parse(RFC1123Format, attrsCopy[XAttrKeyOSSExpires]); err != nil {
			log.LogErrorf("invalid expires format when replicate object [%v] , err: %v", path, err)
			return nil, err
		}
		input.Expires = aws.Time(t)
		delete(attrsCopy, XAttrKeyOSSExpires)
	}
	// TODO  how to parse ACL

	// metadata
	for k, v := range attrsCopy {
		// the ReplicationStatus of replicated objects is always Replica
		if k == VolumeReplicationStatus {
			input.Metadata[k] = aws.String(vol_replication.Replica.String())
			continue
		}

		input.Metadata[k] = aws.String(v)
	}

	return input, nil
}

func ReplicateDeletion(mw *meta.MetaWrapper, inode uint64, volume, path string, targetIDs []string, deletedObjectCreateTime int64) (err error) {
	var w *meta.ReplicationWrapper

	w, err = mw.GetClient(targetIDs[0])
	if err != nil {
		return
	}

	var out *s3.HeadObjectOutput
	out, err = w.Client.HeadObject(&s3.HeadObjectInput{
		Bucket: aws.String(w.TargetConfig.TargetVolume),
		Key:    aws.String(path),
	})

	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			switch aerr.Code() {
			case s3.ErrCodeNoSuchKey, "NotFound":
				if err = mw.RemoveDeletedDentry(inode, path); err != nil {
					log.LogErrorf("RemoveDeletedDentry failed : volume(%v) path(%v) err(%v)", volume, path, err)
				}
				return nil
			default:
				return err
			}
		}
	}

	if ok, err := validDeletionReplication(mw, out, inode, path, deletedObjectCreateTime); !ok {
		return err
	}

	_, err = w.Client.DeleteObject(&s3.DeleteObjectInput{
		Bucket: aws.String(w.TargetConfig.TargetVolume),
		Key:    aws.String(path),
	})

	if err != nil {
		log.LogErrorf("replicateDeletion failed : volume(%v) path(%v) err(%v)", volume, path, err)
		return
	}
	// deletion replicated successfully
	if err = mw.RemoveDeletedDentry(inode, path); err != nil {
		log.LogErrorf("RemoveDeletedDentry failed : volume(%v) path(%v) err(%v)", volume, path, err)
	}

	return

}

func validDeletionReplication(mw *meta.MetaWrapper, out *s3.HeadObjectOutput, inode uint64, path string, deletedObjectCreateTime int64) (valid bool, err error) {

	var targetObjectCreateTime int64
	if objectCreateTimeStr, exist := out.Metadata[VolumeReplicationReplicaCreateTime]; exist {
		if targetObjectCreateTime, err = strconv.ParseInt(*objectCreateTimeStr, 10, 64); err != nil {
			return false, err
		}
	}

	if targetObjectCreateTime == deletedObjectCreateTime {
		return true, nil
	} else if targetObjectCreateTime > deletedObjectCreateTime {
		// stale deletion operation, we are trying to delete a newer object , abort
		// deletion replicated successfully
		if err = mw.RemoveDeletedDentry(inode, path); err != nil {
			return false, err
		}
		return false, nil
	} else {
		// targetObjectCreateTime < deletedObjectCreateTime
		// stale deletion operation, we are trying to delete an older object, need retry
		return false, fmt.Errorf("stale deletion operation, we are trying to delete an older object(%v, createTime:%v), need retry", path, targetObjectCreateTime)
	}
}
