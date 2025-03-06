package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"

	"github.com/confluentinc/confluent-kafka-go/v2/kafka"

	_ "github.com/go-sql-driver/mysql"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/joho/godotenv"
)

type CDCEvent struct {
	Payload struct {
		After  *AfterPayload `json:"after"`
		Op     string        `json:"op"`
		Source struct {
			Snapshot string `json:"snapshot"`
		} `json:"source"`
	} `json:"payload"`
}

type AfterPayload struct {
	UserId int64  `json:"user_id"`
	Name   string `json:"name"`
}

// Configuration constants
const (
	kafkaBroker = "localhost:9093"
	kafkaTopic  = "dbz.testdb.users" // {topic.prefix}{databaseName}{tableName}

	mysqlHost     = "localhost"
	mysqlPort     = "3306"
	mysqlUser     = "root"
	mysqlPassword = "root"
	mysqlDb       = "testdb"

	s3Key         = "backup.csv"
	maxPartSize   = 10 * 1024 * 1024
	maxWriterSize = 500000
)

func main() {
	ctx := context.Background()
	err := godotenv.Load()
	if err != nil {
		log.Fatal("Error loading .env file")
	}
	s3Bucket := os.Getenv("S3_BUCKET")
	s3Region := os.Getenv("S3_REGION")

	// --- Init Kafka ---
	c, err := kafka.NewConsumer(&kafka.ConfigMap{
		"bootstrap.servers": kafkaBroker,
		"group.id":          "my-group2", // can be anything
		"auto.offset.reset": "earliest",
	})
	if err != nil {
		log.Fatalf("failed to create new consumer: %v", err)
	}

	err = c.SubscribeTopics([]string{kafkaTopic}, nil)
	if err != nil {
		log.Fatalf("failed to subscribe to kafka topic: %v", err)
	}

	// --- Init DB ---
	dataSourceName := fmt.Sprintf("%s:%s@tcp(%s:%s)/%s", mysqlUser, mysqlPassword, mysqlHost, mysqlPort, mysqlDb)
	targetDB, err := sql.Open("mysql", dataSourceName)
	if err != nil {
		log.Fatalf("failed to connect to target DB: %v", err)
	}
	defer targetDB.Close()

	// --- Init S3 ---
	buffer := new(bytes.Buffer)
	writer := csv.NewWriter(buffer)
	defer writer.Flush()
	writer.Write([]string{"user_id", "name"})
	writer.Flush()

	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(s3Region))
	if err != nil {
		log.Fatalf("failed to create aws config: %v", err)
	}
	client := s3.NewFromConfig(cfg)

	// Clear existing uploads
	uploads, err := client.ListMultipartUploads(ctx, &s3.ListMultipartUploadsInput{
		Bucket: aws.String(s3Bucket),
	})
	if err != nil {
		log.Printf("error listing multipart uploads: %v\n", err)
	} else {
		for _, upload := range uploads.Uploads {
			log.Printf("key: %s", *upload.Key)
			client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
				Bucket:   aws.String(s3Bucket),
				Key:      upload.Key,
				UploadId: upload.UploadId,
			})
		}
	}

	resp, err := client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket: aws.String(s3Bucket),
		Key:    aws.String(s3Key),
	})
	if err != nil {
		log.Fatalf("failed to create multipart upload: %v", err)
	}
	uploadId := *resp.UploadId

	partNumber := 1
	backupCount := 1
	var partETags []types.CompletedPart

	log.Println("Starting Kafka consumer...")

	for {
		m, err := c.ReadMessage(-1)
		if err != nil {
			log.Printf("error reading message: %v", err)
			continue
		}

		var event CDCEvent
		if err := json.Unmarshal(m.Value, &event); err != nil {
			log.Printf("error unmarshaling JSON: %v", err)
			continue
		}
		if event.Payload.After == nil {
			log.Printf("not a create operation, or no 'after' payload found; skipping message")
			continue
		}

		row := []string{fmt.Sprint(event.Payload.After.UserId), event.Payload.After.Name}
		writer.Write(row)
		backupCount++
		if (backupCount-1)%maxWriterSize == 0 {
			writer.Flush()
		}
		if buffer.Len() > maxPartSize { // If buffer exceeds 10MB
			log.Printf("Uploading part %d\n", partNumber)
			writer.Flush()
			partETag, err := uploadPartToS3(ctx, client, s3Bucket, s3Key, uploadId, partNumber, buffer)
			if err != nil {
				log.Fatalf("error uploading part: %v", err)
			}
			partETags = append(partETags, partETag)
			buffer.Reset()
			partNumber++
		}

		if event.Payload.Source.Snapshot == "last" {
			break
		}
	}

	log.Println("Finished consuming snapshot")

	// Upload the remaining buffer
	writer.Flush()
	partETag, err := uploadPartToS3(ctx, client, s3Bucket, s3Key, uploadId, partNumber, buffer)
	if err != nil {
		log.Fatalf("error uploading part: %v", err)
	}
	partETags = append(partETags, partETag)
	buffer.Reset()

	_, err = client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket:   aws.String(s3Bucket),
		Key:      aws.String(s3Key),
		UploadId: aws.String(uploadId),
		MultipartUpload: &types.CompletedMultipartUpload{
			Parts: partETags,
		},
	})
	if err != nil {
		log.Fatalf("failed to complete multipart upload: %v", err)
	}

	log.Println("Finished S3 backup")
}

func uploadPartToS3(ctx context.Context, client *s3.Client, bucket string, key string, uploadId string, partNumber int, buffer *bytes.Buffer) (types.CompletedPart, error) {
	if buffer.Len() == 0 {
		return types.CompletedPart{}, errors.New("buffer received is empty")
	}

	partInput := &s3.UploadPartInput{
		Bucket:     aws.String(bucket),
		Key:        aws.String(key),
		UploadId:   aws.String(uploadId),
		PartNumber: aws.Int32(int32(partNumber)),
		Body:       bytes.NewReader(buffer.Bytes()),
	}
	resp, err := client.UploadPart(ctx, partInput)
	if err != nil {
		return types.CompletedPart{}, err
	}
	return types.CompletedPart{
		ETag:       resp.ETag,
		PartNumber: aws.Int32(int32(partNumber)),
	}, nil
}
