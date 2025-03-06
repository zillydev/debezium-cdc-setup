# Database CDC Backup Tool

## Overview

This is an example setup that uses Debezium to subscribe to a Kafka topic with Change Data Capture (CDC) events from a MySQL database and creates a backup in CSV format stored on Amazon S3.

## Features

- Consumes CDC events from Kafka (using Debezium format)
- Processes user data changes (inserts)
- Writes data to CSV format
- Uploads CSV files to AWS S3 using multipart upload for large datasets

## Notes

- Minimum size of each part for multipart upload: `5MB`

## Configuration

The following POST call needs to be made to Debezium to register the connector:

```
curl -X POST "http://localhost:8083/connectors" -H "Content-Type: application/json" -d '{
  "name": "mysql-connector",
  "config": {
    "connector.class": "io.debezium.connector.mysql.MySqlConnector",
    "database.hostname": "mysql",
    "database.port": "3306",
    "database.user": "root",
    "database.password": "root",
    "database.server.id": "184054",
    "database.include.list": "testdb",
    "table.include.list": "testdb.users",
    "topic.prefix": "dbz",
    "include.schema.changes": "false",
    "schema.history.internal.kafka.bootstrap.servers": "kafka:9092",
    "schema.history.internal.kafka.topic": "schemahistory.testdb"
  }
}'
```

The following variables must be present in environment to authorise AWS.

```
AWS_ACCESS_KEY_ID=
AWS_SECRET_ACCESS_KEY=
S3_BUCKET=
S3_REGION=
```

## Steps to test

1. `docker-compose up -d`
2. Write this query to MySQL:

    `CREATE TABLE testdb.users (user_id bigint not null, name VARCHAR(100));`

3. Run `go run generate/script.go` to generate dummy data.
4. Make the POST call to Debezium to register the connector.
5. Run `go run main.go`.

## How It Works

1. The application initializes connections to Kafka, MySQL, and AWS S3
2. It subscribes to a Kafka topic containing CDC events in Debezium format
3. For each event, it extracts user_id and name and writes them to a CSV buffer
4. When the buffer reaches a threshold size (10MB), it's uploaded to S3 as a part of a multipart upload
5. After all events are processed, any remaining data is uploaded and the multipart upload is completed
