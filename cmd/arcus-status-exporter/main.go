// arcus-status-exporter observes the existing executor without invoking it,
// opening its signer, taking its lock, or mutating its state.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"reflect"
	"time"

	"debot-dashboard/internal/arcusstatus"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func probe(ctx context.Context, service, timer string) arcusstatus.Units {
	u := arcusstatus.Units{}
	read := func(unit string) map[string]string {
		cmd := exec.CommandContext(ctx, "systemctl", "show", unit, "--property=LoadState,ActiveState,UnitFileState,Result,ExecMainCode,ExecMainStatus,ExecMainStartTimestamp")
		cmd.Env = append(os.Environ(), "LC_ALL=C", "TZ=UTC")
		b, err := cmd.Output()
		if err != nil {
			u.Error = true
			return nil
		}
		return arcusstatus.ParseProperties(b)
	}
	u.Service = read(service)
	u.Timer = read(timer)
	return u
}

func run() error {
	dir := flag.String("state-dir", "/var/lib/debot-arcus/spot-execute-once", "Read-only bot state directory")
	configPath := flag.String("config", "/etc/arcus-spot/config.yaml", "Read-only executor config (only daily limit is projected)")
	service := flag.String("service", "arcus-spot-live-tick.service", "Observed oneshot service")
	timer := flag.String("timer", "arcus-spot-live-tick.timer", "Observed timer")
	bucket := flag.String("s3-bucket", os.Getenv("ARCUS_STATUS_S3_BUCKET"), "Optional destination bucket; without it print JSON only")
	key := flag.String("s3-key", os.Getenv("ARCUS_STATUS_S3_KEY"), "Exact destination object key")
	region := flag.String("s3-region", "eu-central-1", "Destination bucket region")
	flag.Parse()
	if (*bucket == "") != (*key == "") {
		return fmt.Errorf("s3-bucket and s3-key must be set together")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	before := probe(ctx, *service, *timer)
	payload := arcusstatus.Collect(*dir, *configPath, before, time.Now())
	after := probe(ctx, *service, *timer)
	if !reflect.DeepEqual(before, after) {
		payload.Arcus.Healthy = false
		payload.Arcus.HealthReasons = append(payload.Arcus.HealthReasons, "Live-tick service changed during collection; retrying next poll")
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if *bucket == "" {
		_, err = os.Stdout.Write(append(b, '\n'))
		return err
	}
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(*region))
	if err != nil {
		return fmt.Errorf("load AWS configuration: %w", err)
	}
	_, err = s3.NewFromConfig(cfg).PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(*bucket), Key: aws.String(*key), Body: bytes.NewReader(b), ContentType: aws.String("application/json"), CacheControl: aws.String("no-store")})
	if err != nil {
		return fmt.Errorf("publish status: %w", err)
	}
	log.Printf("published Arcus status: health=%s sequence=%d", payload.Arcus.ServiceStatus(time.Now(), arcusstatus.DefaultStaleSecs), payload.Arcus.Sequence)
	return nil
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
