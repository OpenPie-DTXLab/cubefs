package objectnode

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"github.com/aws/aws-sdk-go/aws"
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

func replicateObject(volName string, f *FSFileInfo, w *meta.ReplicationWrapper, metaData map[string]string, reader *io.PipeReader) (err error) {
	var out *s3.PutObjectOutput
	var input *s3.PutObjectInput
	var md5SumIsMatch bool

	if input, err = buildPutObjectInput(f, w, metaData, reader); err != nil {
		log.LogErrorf("buildPutObjectInputMetadata: object: %v/%v failed to replicate to %v , err: %v", volName, f.Path, w.TargetConfig.ID, err)
		return
	}
	out, err = w.Client.PutObject(input)
	if err != nil {
		log.LogErrorf("PutObject: object: %v/%v failed to replicate to %v , err: %v", volName, f.Path, w.TargetConfig.ID, err)
	}

	md5SumIsMatch = *out.ETag == wrapUnescapedQuot(f.ETag)
	if !md5SumIsMatch {
		return fmt.Errorf("object: %v/%v failed to replicate to %v , md5sum mismatch ! ", volName, f.Path, w.TargetConfig.ID)
	}

	return nil
}

func buildPutObjectInput(f *FSFileInfo, w *meta.ReplicationWrapper, metaData map[string]string, reader *io.PipeReader) (input *s3.PutObjectInput, err error) {
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
		Bucket:   aws.String(w.TargetConfig.TargetVolume),
		Key:      aws.String(f.Path),
		Body:     aws.ReadSeekCloser(reader),
		Metadata: map[string]*string{},
	}

	// header
	etag = ParseETagValue(f.ETag)
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
			log.LogErrorf("invalid expires format when replicate object [%v] , err: %v", f.Path, err)
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

func replicateMultiPartsObject(volName string, f *FSFileInfo, w *meta.ReplicationWrapper, metaData map[string]string, reader *io.PipeReader) (err error) {
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
		return fmt.Errorf("replicateMultiPartsObject: object %v/%v  part sizes not found ! ", volName, f.Path)
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
	if totalSize != uint64(f.Size) {
		return fmt.Errorf("object %v/%v bad XAttrKeyOSSPartSizes, total size %v, actually is %v", volName, f.Path, totalSize, f.Size)

	}

	if input, err = buildMultipartUploadInput(f, w, metaData); err != nil {
		return
	}
	attempts := 1
	for attempts <= 3 {
		resp, err = w.Client.CreateMultipartUpload(input)
		if err == nil {
			break
		}
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

func buildMultipartUploadInput(f *FSFileInfo, w *meta.ReplicationWrapper, metaData map[string]string) (input *s3.CreateMultipartUploadInput, err error) {
	input = &s3.CreateMultipartUploadInput{
		Bucket:   aws.String(w.TargetConfig.TargetVolume),
		Key:      aws.String(f.Path),
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
			log.LogErrorf("invalid expires format when replicate object [%v] , err: %v", f.Path, err)
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
