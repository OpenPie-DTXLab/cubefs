package s3

import (
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
)

func CreateReplicationTargetClient(endpoint, accessKey, secretKey string, secure bool, region string) (client *s3.S3) {
	sess := session.Must(session.NewSession())
	var ac = aws.NewConfig()
	ac.Endpoint = aws.String(endpoint)
	ac.DisableSSL = aws.Bool(!secure)
	ac.Region = aws.String(region)
	ac.Credentials = credentials.NewStaticCredentials(accessKey, secretKey, "")
	ac.S3ForcePathStyle = aws.Bool(true)
	client = s3.New(sess, ac)
	return
}
