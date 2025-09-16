<!--

func main() {
	if len(os.Args) > 1 && os.Args[1] == "test" {
		runTest()
		return
	}
	lambda.Start(HandleRequest)
}

func runTest() {
	os.Setenv("soci_index_version", "V1")
	os.Setenv("AWS_REGION", "us-east-1")

	event := events.ECRImageActionEvent{
		Source:     "aws.ecr",
		Account:    "123456789012",
		Region:     "us-east-1",
		DetailType: "ECR Image Action",
		Detail: events.ECRImageActionEventDetail{
			ActionType:     "PUSH",
			Result:         "SUCCESS",
			RepositoryName: "my-test-repo",
			ImageDigest:    "sha256:a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4",
			ImageTag:       "latest",
		},
	}

	ctx := context.Background()
	lc := &lambdacontext.LambdaContext{
		AwsRequestID:       "test-request-123",
		InvokedFunctionArn: "arn:aws:lambda:us-east-1:123456789012:function:soci-index-generator",
		Identity:           lambdacontext.CognitoIdentity{},
		ClientContext:      lambdacontext.ClientContext{},
	}
	ctx = lambdacontext.NewContext(ctx, lc)

	fmt.Println("Testing lambda handler...")

	result, err := HandleRequest(ctx, event)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	fmt.Printf("Success: %s\n", result)
} -->
