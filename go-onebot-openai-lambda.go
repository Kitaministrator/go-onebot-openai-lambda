package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	openai "github.com/sashabaranov/go-openai"
)

type Response events.APIGatewayProxyResponse

var extraLog = os.Getenv("EXTRA_LOG") // string, true, false or (null)
var openaiApiKey = os.Getenv("OPENAI_API_KEY")
var envMaxRetries = os.Getenv("MAX_RETRIES")
var envRetryDelay = os.Getenv("RETRY_DELAY")
var receiverAddr = os.Getenv("RECV_ADDR") // http://1.2.3.4/port

type OnebotGroupMessageRequestBody struct {
	PostType    string `json:"post_type"`
	MessageType string `json:"message_type"`
	Time        int64  `json:"time"`
	SelfID      int64  `json:"self_id"`
	SubType     string `json:"sub_type"`
	Message     string `json:"message"`
	MessageSeq  int64  `json:"message_seq"`
	Sender      struct {
		Age      int64  `json:"age"`
		Area     string `json:"area"`
		Card     string `json:"card"`
		Level    string `json:"level"`
		Nickname string `json:"nickname"`
		Role     string `json:"role"`
		Sex      string `json:"sex"`
		Title    string `json:"title"`
		UserID   int64  `json:"user_id"`
	} `json:"sender"`
	UserID     uint64      `json:"user_id"`
	Anonymous  interface{} `json:"anonymous"`
	GroupID    uint64      `json:"group_id"`
	RawMessage string      `json:"raw_message"`
	MessageID  int64       `json:"message_id"`
	Font       int64       `json:"font"`
}

func main() {
	lambda.Start(HandleRequest)
}

func HandleRequest(ctx context.Context, req events.APIGatewayProxyRequest) error {
	// Parse incoming request body
	var onebotGrpMsgReqBody OnebotGroupMessageRequestBody
	errUnmarshal := json.Unmarshal([]byte(req.Body), &onebotGrpMsgReqBody)
	if errUnmarshal != nil {
		log.Println(errUnmarshal)
		return errUnmarshal
	}

	// Get clean message and group & user id
	re := regexp.MustCompile(`\[CQ:at,qq=\d+\]`)
	onebotMessageContext := re.ReplaceAllString(onebotGrpMsgReqBody.RawMessage, "")
	gid := onebotGrpMsgReqBody.GroupID
	uid := onebotGrpMsgReqBody.UserID

	// Debug output
	if extraLog == "true" {
		// check basic properties
		log.Println("Method: " + req.HTTPMethod)                                   // GET or POST or ...
		log.Println("req.Path: " + req.Path)                                       // /stage/route/proxy_path
		log.Println("RequestContext.Protocol: " + req.RequestContext.Protocol)     // HTTP/1.1
		log.Println("RequestContext.DomainName: " + req.RequestContext.DomainName) // my-domain.execute-api.some-region.amazonaws.com
		log.Println("RequestContext.ResourceID: " + req.RequestContext.ResourceID) // ANY /route/{proxy+}

		// print req headers
		log.Println("========  Start to print headers from API Gateway  ========")
		for k, v := range req.Headers {
			log.Println("k: " + k + ", v: " + v)
		}
		log.Println("========  End of printing headers from API Gateway  ========")

		// print req body
		log.Println("========  Start to print body from API Gateway  ========")
		log.Println(req.Body)
		log.Println("========  End of printing body from API Gateway  ========")

		// print clean message
		log.Println("Clean message: " + onebotMessageContext)
	}

	// Send to OpenAI with error retry
	maxRetries, errParseInt := strconv.Atoi(envMaxRetries)
	if errParseInt != nil {
		maxRetries = 3
		log.Printf("Failed to parse env MAX_RETRIES: %s. Using default value of %d.\n", errParseInt, maxRetries)
	}

	retryDelay, errParseInt2 := strconv.Atoi(envRetryDelay)
	if errParseInt2 != nil {
		retryDelay = 5
		log.Printf("Failed to parse env RETRY_DELAY: %s. Using default value of %d.\n", errParseInt2, retryDelay)
	}

	var errOpenai error
	resp := ""
	for retryCounts := 0; retryCounts < maxRetries; retryCounts++ {
		resp, errOpenai = SendChatCompletion(onebotMessageContext, openai.GPT40314)
		if errOpenai == nil {
			break
		}
		time.Sleep(time.Duration(retryDelay) * time.Second)
	}

	// Switch to try GPT-3.5 after max retries reached
	if errOpenai != nil {
		// reset error flag
		errOpenai = nil
		log.Println("Call for GPT-4's endpoint failed, now switch to GPT-3.5's.")

		for retryCounts := 0; retryCounts < maxRetries; retryCounts++ {
			resp, errOpenai = SendChatCompletion(onebotMessageContext, openai.GPT3Dot5Turbo0301)
			if errOpenai == nil {
				resp = "(对话降级至GPT-3.5)\n" + resp
				break
			}
			time.Sleep(time.Duration(retryDelay) * time.Second)
		}
		if errOpenai != nil {
			log.Println("All attempts for GPT-4/3.5 failed.")
			resp = "GPT-4、GPT-3.5尝试均失败，请稍后再试。"
		}
	}

	// Direct send to Onebot Http Server
	var errSendBack error
	for retryCounts := 0; retryCounts < maxRetries; retryCounts++ {
		errSendBack = SendGroupMessageBack(resp, gid, uid)
		if errSendBack == nil {
			break
		}
		time.Sleep(time.Duration(retryDelay) * time.Second)
	}
	if errOpenai != nil {
		log.Println("Send back to Onebot API failed.")
	}

	return nil
}

func SendGroupMessageBack(msg string, gid uint64, uid uint64) error {
	type tMessage struct {
		Type string      `json:"type"`
		Data interface{} `json:"data"`
	}
	type tRequestBody struct {
		GroupID uint64     `json:"group_id"`
		Message []tMessage `json:"message"`
	}
	url := receiverAddr + "/send_group_msg"

	data := tRequestBody{
		GroupID: gid,
		Message: []tMessage{
			{
				Type: "at",
				Data: map[string]string{
					"qq": strconv.FormatUint(uid, 10),
				},
			},
			{
				Type: "text",
				Data: map[string]string{
					"text": msg,
				},
			},
		},
	}

	jsonData, err := json.Marshal(data)
	if err != nil {
		log.Println("Error marshalling JSON:", err)
		return err
	}

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		log.Println("Error creating request:", err)
		return err
	}

	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		log.Println("Error making request:", err)
		return err
	}

	defer resp.Body.Close()

	log.Println("Request successful:", resp.Status)

	return nil
}

func SendChatCompletion(prompt string, model string) (string, error) {
	// Send to OpenAI API, this method only for GPT-4 or GPT-3.5-Turbo
	openaiClient := openai.NewClient(openaiApiKey)
	openaiResp, err := openaiClient.CreateChatCompletion(
		context.Background(),
		openai.ChatCompletionRequest{
			Model: model,
			Messages: []openai.ChatCompletionMessage{
				{
					Role:    openai.ChatMessageRoleUser,
					Content: prompt,
				},
			},
		},
	)

	if err != nil {
		log.Printf("ChatCompletion error: %v\n", err)
		return "", err
	}

	if extraLog == "true" {
		log.Println("Openai response message name: " + openaiResp.Choices[0].Message.Name)
		log.Println("Openai response message role: " + openaiResp.Choices[0].Message.Role)
		log.Println("Openai response message content: " + openaiResp.Choices[0].Message.Content)
	}

	return openaiResp.Choices[0].Message.Content, nil
}
