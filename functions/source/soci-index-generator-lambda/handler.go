// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"errors"
	"path"

	"github.com/aws-ia/cfn-aws-soci-index-builder/soci-index-generator-lambda/events"
	"github.com/aws-ia/cfn-aws-soci-index-builder/soci-index-generator-lambda/utils/fs"
	"github.com/aws-ia/cfn-aws-soci-index-builder/soci-index-generator-lambda/utils/log"
	registryutils "github.com/aws-ia/cfn-aws-soci-index-builder/soci-index-generator-lambda/utils/registry"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-lambda-go/lambdacontext"
	"github.com/containerd/containerd/images"
	"oras.land/oras-go/v2/content/oci"

	"github.com/awslabs/soci-snapshotter/soci"
	"github.com/awslabs/soci-snapshotter/soci/store"
	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/content/local"
	"github.com/containerd/containerd/platforms"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

var (
	ErrEmptyIndex = errors.New("no ztocs created, all layers either skipped or produced errors")
)

const (
	BuildFailedMessage          = "SOCI index build error"
	TagFailedMessage            = "SOCI V2 OCI Image tag error"
	PushFailedMessage           = "SOCI index push error"
	SkipPushOnEmptyIndexMessage = "Skipping pushing SOCI index as it does not contain any zTOCs"
	BuildAndPushSuccessMessage  = "Successfully built and pushed SOCI index"

	artifactsStoreName = "store"
	artifactsDbName    = "artifacts.db"
)

type contextKey string

const (
	RegistryURLKey     contextKey = "RegistryURL"
	RepositoryNameKey  contextKey = "RepositoryName"
	ImageDigestKey     contextKey = "ImageDigest"
	ImageTagKey        contextKey = "ImageTag"
	SOCIIndexDigestKey contextKey = "SOCIIndexDigest"
	SociIndexVersion   string     = "soci_index_version"
)

func HandleRequest(ctx context.Context, event events.ECRImageActionEvent) (string, error) {
	ctx, err := validateEvent(ctx, event)
	if err != nil {
		return lambdaError(ctx, "ECRImageActionEvent validation error", err)
	}

	sociIndexVersion := os.Getenv(SociIndexVersion)
	log.Info(ctx, fmt.Sprintf("Using SOCI index version: %s", sociIndexVersion))

	repo := event.Detail.RepositoryName
	digest := event.Detail.ImageDigest
	registryUrl := buildEcrRegistryUrl(event)
	ctx = context.WithValue(ctx, RegistryURLKey, registryUrl)

	registry, err := registryutils.Init(ctx, registryUrl)
	if err != nil {
		return lambdaError(ctx, "Remote registry initialization error", err)
	}

	err = registry.ValidateImageDigest(ctx, repo, digest, sociIndexVersion)
	if err != nil {
		log.Warn(ctx, fmt.Sprintf("Image manifest validation error: %v", err))
		return "Exited early due to manifest validation error", nil
	}

	var tag string
	if sociIndexVersion == "V2" {
		originalTag, ok := ctx.Value(ImageTagKey).(string)
		if !ok || originalTag == "" {
			log.Info(ctx, "Skipping SOCI index generation for V2 as image has no tag")
			return "Skipped SOCI index generation for V2 as image has no tag", nil
		}

		tag = originalTag + "-soci"
		log.Info(ctx, fmt.Sprintf("Using original image tag with suffix: %s", tag))
	}

	dataDir, err := createTempDir(ctx)
	if err != nil {
		return lambdaError(ctx, "Directory create error", err)
	}
	defer cleanUp(ctx, dataDir)

	quitChannel := make(chan int)
	defer func() {
		quitChannel <- 1
	}()

	setDeadline(ctx, quitChannel, dataDir)

	sociStore, err := initSociStore(ctx, dataDir)
	if err != nil {
		return lambdaError(ctx, "OCI storage initialization error", err)
	}

	desc, err := registry.Pull(ctx, repo, sociStore, digest)
	if err != nil {
		return lambdaError(ctx, "Image pull error", err)
	}

	image := images.Image{
		Name:   repo + "@" + digest,
		Target: *desc,
	}

	indexDescriptor, err := buildIndex(ctx, dataDir, sociStore, image, sociIndexVersion)
	if err != nil {
		if err.Error() == ErrEmptyIndex.Error() {
			log.Warn(ctx, SkipPushOnEmptyIndexMessage)
			return SkipPushOnEmptyIndexMessage, nil
		}
		return lambdaError(ctx, BuildFailedMessage, err)
	}

	ctx = context.WithValue(ctx, SOCIIndexDigestKey, indexDescriptor.Digest.String())

	err = registry.Push(ctx, sociStore, *indexDescriptor, repo, tag)
	if err != nil {
		return lambdaError(ctx, PushFailedMessage, err)
	}

	log.Info(ctx, BuildAndPushSuccessMessage)
	return BuildAndPushSuccessMessage, nil
}

func validateEvent(ctx context.Context, event events.ECRImageActionEvent) (context.Context, error) {
	var errors []error

	if event.Source != "aws.ecr" {
		errors = append(errors, fmt.Errorf("the event's 'source' must be 'aws.ecr'"))
	}
	if event.Account == "" {
		errors = append(errors, fmt.Errorf("the event's 'account' must not be empty"))
	}
	if event.DetailType != "ECR Image Action" {
		errors = append(errors, fmt.Errorf("the event's 'detail-type' must be 'ECR Image Action'"))
	}
	if event.Detail.ActionType != "PUSH" {
		errors = append(errors, fmt.Errorf("the event's 'detail.action-type' must be 'PUSH'"))
	}
	if event.Detail.Result != "SUCCESS" {
		errors = append(errors, fmt.Errorf("the event's 'detail.result' must be 'SUCCESS'"))
	}
	if event.Detail.RepositoryName == "" {
		errors = append(errors, fmt.Errorf("the event's 'detail.repository-name' must not be empty"))
	}
	if event.Detail.ImageDigest == "" {
		errors = append(errors, fmt.Errorf("the event's 'detail.image-digest' must not be empty"))
	}

	validAccountId, err := regexp.MatchString(`[0-9]{12}`, event.Account)
	if err != nil {
		errors = append(errors, err)
	}
	if !validAccountId {
		errors = append(errors, fmt.Errorf("the event's 'account' must be a valid AWS account ID"))
	}

	validRepositoryName, err := regexp.MatchString(`(?:[a-z0-9]+(?:[._-][a-z0-9]+)*/)*[a-z0-9]+(?:[._-][a-z0-9]+)*`, event.Detail.RepositoryName)
	if err != nil {
		errors = append(errors, err)
	}
	if validRepositoryName {
		ctx = context.WithValue(ctx, RepositoryNameKey, event.Detail.RepositoryName)
	} else {
		errors = append(errors, fmt.Errorf("the event's 'detail.repository-name' must be a valid repository name"))
	}

	validImageDigest, err := regexp.MatchString(`[[A-Za-z][A-Za-z0-9]*(?:[-_+.][A-Za-z][A-Za-z0-9]*)*[:][A-Fa-f0-9]{32,}`, event.Detail.ImageDigest)
	if err != nil {
		errors = append(errors, err)
	}
	if validImageDigest {
		ctx = context.WithValue(ctx, ImageDigestKey, event.Detail.ImageDigest)
	} else {
		errors = append(errors, fmt.Errorf("the event's 'detail.image-digest' must be a valid image digest"))
	}

	if event.Detail.ImageTag != "" {
		validImageTag, err := regexp.MatchString(`[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}`, event.Detail.ImageTag)
		if err != nil {
			errors = append(errors, err)
		}
		if validImageTag {
			ctx = context.WithValue(ctx, ImageTagKey, event.Detail.ImageTag)
		} else {
			errors = append(errors, fmt.Errorf("the event's 'detail.image-tag' must be empty or a valid image tag"))
		}
	}

	if len(errors) == 0 {
		return ctx, nil
	} else {
		return ctx, errors[0]
	}
}

func buildEcrRegistryUrl(event events.ECRImageActionEvent) string {
	var awsDomain = ".amazonaws.com"
	if strings.HasPrefix(event.Region, "cn") {
		awsDomain = ".amazonaws.com.cn"
	}
	return event.Account + ".dkr.ecr." + event.Region + awsDomain
}

func createTempDir(ctx context.Context) (string, error) {
	freeSpace := fs.CalculateFreeSpace("/tmp")
	log.Info(ctx, fmt.Sprintf("There are %d bytes of free space in /tmp directory", freeSpace))
	if freeSpace < 6_000_000_000 {
		log.Warn(ctx, fmt.Sprintf("Free space in /tmp is only %d bytes, which is less than 6GB", freeSpace))
	}

	log.Info(ctx, "Creating a directory to store images and SOCI artifacts")
	lambdaContext, _ := lambdacontext.FromContext(ctx)
	tempDir, err := os.MkdirTemp("/tmp", lambdaContext.AwsRequestID)
	return tempDir, err
}

func cleanUp(ctx context.Context, dataDir string) {
	log.Info(ctx, fmt.Sprintf("Removing all files in %s", dataDir))
	if err := os.RemoveAll(dataDir); err != nil {
		log.Error(ctx, "Clean up error", err)
	}
}

func setDeadline(ctx context.Context, quitChannel chan int, dataDir string) {
	deadline, _ := ctx.Deadline()
	deadline = deadline.Add(-10 * time.Second)
	timeoutChannel := time.After(time.Until(deadline))
	go func() {
		for {
			select {
			case <-timeoutChannel:
				cleanUp(ctx, dataDir)
				log.Error(ctx, "Invocation timeout error", fmt.Errorf("invocation timeout after 14 minutes and 50 seconds"))
				return
			case <-quitChannel:
				return
			}
		}
	}()
}

func initContainerdStore(dataDir string) (content.Store, error) {
	containerdStore, err := local.NewStore(path.Join(dataDir, artifactsStoreName))
	return containerdStore, err
}

func initSociStore(ctx context.Context, dataDir string) (*store.SociStore, error) {
	ociStore, err := oci.NewWithContext(ctx, path.Join(dataDir, artifactsStoreName))
	return &store.SociStore{
		Store: ociStore,
	}, err
}

func initSociArtifactsDb(dataDir string) (*soci.ArtifactsDb, error) {
	artifactsDbPath := path.Join(dataDir, artifactsDbName)
	artifactsDb, err := soci.NewDB(artifactsDbPath)
	if err != nil {
		return nil, err
	}
	return artifactsDb, nil
}

func buildIndex(ctx context.Context, dataDir string, sociStore *store.SociStore, image images.Image, sociIndexVersion string) (*ocispec.Descriptor, error) {
	log.Info(ctx, "Building SOCI index")
	platform := platforms.DefaultSpec()

	artifactsDb, err := initSociArtifactsDb(dataDir)
	if err != nil {
		return nil, err
	}

	containerdStore, err := initContainerdStore(dataDir)
	if err != nil {
		return nil, err
	}

	builderOpts := []soci.BuildOption{
		soci.WithBuildToolIdentifier("AWS SOCI Index Builder Cfn v0.2"),
	}

	builder, err := soci.NewIndexBuilder(containerdStore, sociStore, artifactsDb, builderOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create index builder: %w", err)
	}

	if sociIndexVersion == "V2" {
		_, err := builder.Build(ctx, image)
		if err != nil {
			return nil, fmt.Errorf("failed to build V2 SOCI index: %w", err)
		}
		fmt.Printf("Generated V2 Index\n")
		desc := ocispec.Descriptor{
			MediaType: "application/vnd.oci.image.index.v1+json",
			Digest:    "sha256:placeholder",
			Size:      0,
		}
		return &desc, nil
	} else {
		_, err := builder.Build(ctx, image)
		if err != nil {
			return nil, fmt.Errorf("failed to build SOCI index: %w", err)
		}
		fmt.Printf("Generated SOCI Index\n")
		indexDescriptorInfos, _, err := soci.GetIndexDescriptorCollection(ctx, containerdStore, artifactsDb, image, []ocispec.Platform{platform})
		if err != nil {
			return nil, err
		}
		if len(indexDescriptorInfos) == 0 {
			return nil, errors.New("no SOCI indices found in OCI store")
		}
		sort.Slice(indexDescriptorInfos, func(i, j int) bool {
			return indexDescriptorInfos[i].CreatedAt.Before(indexDescriptorInfos[j].CreatedAt)
		})

		return &indexDescriptorInfos[len(indexDescriptorInfos)-1].Descriptor, nil
	}
}

func lambdaError(ctx context.Context, msg string, err error) (string, error) {
	log.Error(ctx, msg, err)
	return msg, err
}

func main() {
	lambda.Start(HandleRequest)
}