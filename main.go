package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	openai "github.com/sashabaranov/go-openai"
	yaml "gopkg.in/yaml.v2"
)

func fatalf(msg string, a ...any) {
	fmt.Fprintf(os.Stderr, msg, a...)
	os.Exit(1)

}

func isPackageDeclaration(line string) bool {
	return strings.HasPrefix(line, "package ")
}

func isGPTAddedCodeBlockDelimeter(line string) bool {
	return strings.HasPrefix(line, "```")
}

func startsImportBlock(line string) bool {
	return strings.HasPrefix(line, "import (")
}

func isSingleImportStatement(line string) bool {
	return strings.HasPrefix(line, "import \"")
}

func handleImportBlock(lines []string, imports *strings.Builder, importSet map[string]struct{}) int {
	for i, line := range lines {
		trimmedLine := strings.TrimSpace(line)
		if strings.HasPrefix(trimmedLine, ")") {
			return i + 1
		}
		addImport(imports, trimmedLine, importSet)
	}
	panic("import block not closed")
}

func addImport(imports *strings.Builder, importLine string, importSet map[string]struct{}) {
	if _, exists := importSet[importLine]; !exists {
		imports.WriteString("\t" + importLine + "\n")
		importSet[importLine] = struct{}{}
	}
}

func combineSections(packageDecl, imports, functions string) string {
	var combined strings.Builder
	if packageDecl != "" {
		combined.WriteString("package " + packageDecl + "\n")
	}
	combined.WriteString("\n")
	if imports != "" {
		combined.WriteString("\nimport (\n")
		combined.WriteString(imports)
		combined.WriteString(")\n\n")
	}
	combined.WriteString(functions)
	return combined.String()
}

// AggregateFiles combines responses into a single string, ensuring that the output is a valid Go tests file.
func AggregateFiles(pkgName string, fs []string, comment bool) string {
	var imports strings.Builder
	var functions strings.Builder

	importSet := make(map[string]struct{})

	for _, response := range fs {
		lines := strings.Split(response, "\n")

		for i := 0; i < len(lines); i++ {
			line := lines[i]
			trimmedLine := strings.TrimSpace(line)

			switch {
			case isPackageDeclaration(trimmedLine), isGPTAddedCodeBlockDelimeter(trimmedLine):
				continue

			case startsImportBlock(trimmedLine):
				i += handleImportBlock(lines[i+1:], &imports, importSet)

			case isSingleImportStatement(trimmedLine):
				addImport(&imports, strings.TrimPrefix(trimmedLine, "import "), importSet)

			default:

				// Appending function bodies
				if comment {
					functions.WriteString("// ")
				}
				functions.WriteString(line + "\n")
			}
		}
		functions.WriteString("\n")
	}

	return combineSections(pkgName, imports.String(), functions.String())
}

// ConcatFiles combines multiple code files into a single string.
func ConcatFiles(fs []string) (pkgName string, files string, err error) {
	// TODO: Summarize methods and dependencies as signatures

	var rfs []string

	for i, f := range fs {
		fc, err := os.ReadFile(f)
		if err != nil {
			return "", "", err
		}
		rfs = append(rfs, string(fc))
		if i == 0 {
			scanner := bufio.NewScanner(bytes.NewReader(fc))
			for scanner.Scan() {
				line := scanner.Text()
				if strings.HasPrefix(line, "package ") {
					pkgName = strings.TrimPrefix(line, "package ")
					break
				}
			}
			if err := scanner.Err(); err != nil {
				return "", "", err
			}
		}

	}
	s := AggregateFiles(pkgName, rfs, false)

	return pkgName, s, nil
}

// Client is a client for interacting with the OpenAI API.
type Client struct {
	model     string
	maxTokens uint
	client    *openai.Client
}

// NewClient initializes a new OpenAI API client.
func NewClient(model string, maxTokens int) (*Client, error) {
	k := os.Getenv("OPENAI_API_KEY")
	if k == "" {
		return nil, errors.New("no OpenAI API key provided")
	}
	c := openai.NewClient(k)
	if maxTokens == 0 {
		if model == openai.GPT4 {
			maxTokens = 4000
		} else {
			maxTokens = 2048
		}
	}

	return &Client{
		model,
		uint(maxTokens),
		c,
	}, nil
}

const SectionSeparator = "*************************************************************************"

func (c *Client) BasicCompletionRequest() openai.ChatCompletionRequest {
	return openai.ChatCompletionRequest{
		Model:     c.model,
		MaxTokens: int(c.maxTokens),
	}
}

func (c *Client) CreateChatCompletion(
	ctx context.Context,
	req openai.ChatCompletionRequest,
) (response *openai.ChatCompletionResponse, err error) {
	resp, err := c.client.CreateChatCompletion(ctx, req)
	if err != nil {
		apiErr, ok := err.(*openai.APIError)
		if ok && (apiErr.HTTPStatusCode == 429 || apiErr.HTTPStatusCode >= 500) {
			const backoffSeconds = 10
			fmt.Printf("Rate limit exceeded, waiting %d seconds...\n", backoffSeconds)
			time.Sleep(backoffSeconds * time.Second)

			resp, err := c.client.CreateChatCompletion(ctx, req)
			if err != nil {
				return nil, err
			}
			return &resp, nil

		}
		return nil, err
	}
	return &resp, nil
}

func promptForSpec(whatToTest string, allCode string, extraInstructions string) []openai.ChatCompletionMessage {
	systemContent := "Acting as a senior software engineer you should make a step-by-step description for the user's code focusing on the specified part."

	systemMsg := openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleSystem,
		Content: systemContent,
	}

	userContent := fmt.Sprintf(
		"Based on the provided code write a specification for the `%s` part.\n"+
			"The code is: \n```go\n%s```\n",
		whatToTest,
		allCode,
	)
	if extraInstructions != "" {
		userContent += "\n" + extraInstructions
	}
	userMsg := openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleUser,
		Content: userContent,
	}
	log.Println("System spec message:", systemMsg)
	log.Println("User spec message:", userMsg)
	return []openai.ChatCompletionMessage{
		systemMsg,
		userMsg,
	}
}

func (c *Client) GenerateSpec(whatToTest string, allCode string, extraInstructions string) (string, error) {
	log.Println(SectionSeparator)
	log.Println("Generating spec for", whatToTest)
	ctx := context.Background()

	req := c.BasicCompletionRequest()
	// req.Temperature = 0.8
	// req.TopP = 1
	req.Messages = promptForSpec(whatToTest, allCode, extraInstructions)

	stream, err := c.client.CreateChatCompletionStream(ctx, req)
	if err != nil {
		return "", err
	}
	var result string
	defer stream.Close()
	for {
		response, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return result, nil
		}

		if err != nil {
			return "", err
		}

		fmt.Printf(response.Choices[0].Delta.Content)
		result += response.Choices[0].Delta.Content
	}
}

func promptTestsList(whatToTest string, allCode string, extraInstructions string) []openai.ChatCompletionMessage {
	systemContent := "Acting as a senior software engineer " +
		"you should create an exhaustive and comprehensive list of tests to implement " +
		"that would do full code coverage for the specified part of the code.\n" +
		"Each test case should test only one concrete case. Return the list of descriptive test names."

	userContent := fmt.Sprintf(
		"I want to test '%s'.\n"+
			"The code is: \n```go\n%s```\n",
		whatToTest,
		allCode,
	)
	if extraInstructions != "" {
		userContent += "\n" + extraInstructions
	}

	systemMsg := openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleSystem,
		Content: systemContent,
	}

	userMsg := openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleUser,
		Content: userContent,
	}
	log.Println("Generatin list of tests")
	log.Println("System spec message:", systemMsg)
	log.Println("User spec message:", userMsg)
	return []openai.ChatCompletionMessage{
		systemMsg,
		userMsg,
	}
}

func (c *Client) GenerateTestsList(whatToTest string, allCode string, extraInstructions string) (string, error) {
	log.Println(SectionSeparator)
	log.Println("Generating tests list for ", whatToTest)
	ctx := context.Background()

	req := c.BasicCompletionRequest()
	// req.Temperature = 0.8
	// req.TopP = 1
	req.Messages = promptTestsList(whatToTest, allCode, extraInstructions)

	stream, err := c.client.CreateChatCompletionStream(ctx, req)
	if err != nil {
		return "", err
	}
	var result string
	defer stream.Close()
	for {
		response, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return result, nil
		}

		if err != nil {
			return "", err
		}

		fmt.Printf(response.Choices[0].Delta.Content)
		result += response.Choices[0].Delta.Content
	}
}

const yamlExample = `cases:
  - 
    name: TestThing_Condition1
    instructions: |
      1. Intialize mocks or input data
      2. Execute the tested method
      3. Expect the result to be equal to the expected value and all other expectations are met
  
  - 
    name: TestThing_Condition2
    instructions: TODO
  
  - 
    name: TestThing_Action3_WhenSomething
    instructions: TODO

`

func promptForTestCases(_ string, allCode string, list string, extraInstructions string) []openai.ChatCompletionMessage {
	systemContent := fmt.Sprintf("Acting as a seniour developer "+
		"you should read given code and create instructions to implement the tests.\n"+
		"Using YAML format you should only write `cases` list with the `name` and `instructions` fields.\n"+
		"`instructions` field should contain precise input description and output and/or mock expectations based on the provided code.\n"+
		"Example schema: \n```yaml\n%s\n```\n", yamlExample)

	systemMsg := openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleSystem,
		Content: systemContent,
	}

	userContent := fmt.Sprintf(
		"Here is my code: \n```go\n%s```\n"+
			"Refine these tests: \n\"\"\"%s\"\"\"\n",
		allCode,
		list,
	)
	if extraInstructions != "" {
		userContent += "\n" + extraInstructions
	}
	userMsg := openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleUser,
		Content: userContent,
	}
	log.Println("System message:", systemMsg)
	log.Println("User message:", userMsg)
	return []openai.ChatCompletionMessage{
		systemMsg,
		userMsg,
	}
}

func (c *Client) GenerateTestCases(whatToTest string, allCode string, testList string, extraInstructions string) (string, error) {
	log.Println(SectionSeparator)
	fmt.Println("Generating test cases")

	// TODO: First generate just the text from multiple perspectives and merge it and then map it to yaml format
	ctx := context.Background()

	req := c.BasicCompletionRequest()
	// req.Temperature = 0.8
	// req.TopP = 1
	req.Messages = promptForTestCases(whatToTest, allCode, testList, extraInstructions)

	stream, err := c.client.CreateChatCompletionStream(ctx, req)
	if err != nil {
		return "", err
	}
	var result string
	defer stream.Close()
	for {
		response, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return result, nil
		}

		if err != nil {
			return "", err
		}

		fmt.Printf(response.Choices[0].Delta.Content)
		result += response.Choices[0].Delta.Content
	}
}

func mocksGenerationPromptSystem() string {
	return "Acting as a senior software engineer should implement mocks to test the specific part of the code." +
		"You may use github.com/stretchr/testify/mock. You should not write the tests itself, " +
		"only implement mocks for dependencies of the code that needs to be tested, " +
		"not the mock of the target method/struct but the mocks of the input/dependencies."
}

func mocksGenerationPromptUser(whatToTest string, allTheCode string) string {
	return fmt.Sprintf(
		"We want to test the '%s' part that so please create mocks for the future tests.\n"+
			"Here is the original code: ```go\n%s```\n",
		whatToTest,
		allTheCode,
	)
}

func (c *Client) GenerateMocks(
	whatToTest string,
	allCode string,
	extraInstructions string,
) (string, error) {
	ctx := context.Background()

	systemContent := mocksGenerationPromptSystem()
	userContent := mocksGenerationPromptUser(whatToTest, allCode)

	log.Println(SectionSeparator)
	log.Println("Mocks generation system prompt: ", systemContent)
	log.Println("Mocks generation user prompt: ", userContent)
	if extraInstructions != "" {
		userContent += "\n" + extraInstructions
	}
	req := c.BasicCompletionRequest()
	req.Temperature = 0
	req.TopP = 1
	req.Messages = []openai.ChatCompletionMessage{
		{
			Role:    openai.ChatMessageRoleSystem,
			Content: systemContent,
		},
		{
			Role:    openai.ChatMessageRoleUser,
			Content: userContent,
		},
	}

	resp, err := c.CreateChatCompletion(ctx, req)
	if err != nil {
		return "", err
	}

	return resp.Choices[0].Message.Content, nil
}

func commentLines(text string) string {
	lines := strings.Split(text, "\n")
	commentedText := ""

	for _, line := range lines {
		commentedLine := "// " + line
		commentedText += commentedLine + "\n"
	}

	return commentedText
}

const codeTemplate = `package %s

// Use this libs if needed
import (
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require
)

func %s(t *testing.T) {
}
`

func codeHeader(pkgName string) string {
	return fmt.Sprintf("package %s\n\n", pkgName)
}

// TODO: Extract the code an polish it with gpt3.5
func codeGenerationPrompt(_ string, spec Spec, allTheCode string, pkg string) string {
	return fmt.Sprintf(
		"Act as a senior developer.\n"+
			"Based on this code: ```go\n%s```\nHelp me to implement a test function, replace the comments with your own code in this snippet: \n```go\n%s\n```",
		allTheCode,
		fmt.Sprintf(codeTemplate, pkg, spec.Name),
	)
}

// Spec represents a single test specification.
type Spec struct {
	Name        string `yaml:"name"`
	Description string `yaml:"instructions"`
}

// GenerateTestCode generates test code using the OpenAI chat completion API.
func (c *Client) GenerateTestCode(
	spec Spec,
	whatToTest string,
	allCode string,
	pkg string,
	extraInstructions string,
) (string, error) {
	ctx := context.Background()

	content := codeGenerationPrompt(whatToTest, spec, allCode, pkg)
	if extraInstructions != "" {
		content += "\n" + extraInstructions
	}

	log.Println("Code generation prompt: ", content)
	msg := openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleSystem,
		Content: content,
	}

	req := c.BasicCompletionRequest()
	req.Temperature = 0
	req.TopP = 1
	req.Messages = []openai.ChatCompletionMessage{
		msg,
	}

	resp, err := c.CreateChatCompletion(ctx, req)
	if err != nil {
		return "", err
	}

	return resp.Choices[0].Message.Content, nil
}

// WriteToFile writes the combined responses into a file.
func WriteToFile(out string, fPath string) error {
	file, err := os.OpenFile(fPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("failed to open file for writing: %v", err)
	}
	defer file.Close()

	_, err = file.WriteString(out)
	if err != nil {
		return fmt.Errorf("failed to write to file: %v", err)
	}

	err = file.Sync()
	if err != nil {
		return fmt.Errorf("failed to flush to disk: %v", err)
	}

	return nil
}

// SpecList wraps the array of Specs for unmarshalling from YAML
type SpecList struct {
	Testing string `yaml:"testing"`
	Specs   []Spec `yaml:"cases"`
}

// LoadTestSpecs loads test specifications from a file.
func LoadTestSpecs(fPath string) (*SpecList, error) {
	file, err := os.Open(fPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	content, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}

	var specList SpecList
	err = yaml.Unmarshal(content, &specList)
	if err != nil {
		return nil, err
	}

	return &specList, nil
}

func removeYamlLines(input string) string {
	lines := strings.Split(input, "\n")
	filtered := make([]string, 0, len(lines))

	for _, line := range lines {
		if !strings.Contains(line, "```yaml") && !strings.Contains(line, "```") {
			filtered = append(filtered, line)
		}
	}

	return strings.Join(filtered, "\n")
}

func main() {
	logFile, err := os.OpenFile("./goptest-debug.log", os.O_APPEND|os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		log.Panic(err)
	}
	defer logFile.Close()
	log.SetOutput(logFile)
	log.SetFlags(log.Lshortfile | log.LstdFlags)

	specFilePath := flag.String("spec-file", "", "Path to the spec file")
	codeFiles := flag.String("code-files", "", "Comma-separated paths to code files")
	outputFilePath := flag.String("output-file", "", "Path to output file")
	cases := flag.Bool("cases", false, "Generate cases or not, default false")
	whatToTest := flag.String("what", "", "What to test")
	model := flag.String("model", "gpt-4", "Model to use")
	maxTokens := flag.Int("max-tokens", 4000, "Maximum tokens for output")
	extraInstructions := flag.String("extra", "", "Extra instructions for the model")
	flag.Parse()

	if *specFilePath == "" || *codeFiles == "" {
		fatalf("spec-file, code-files, and output-file must be provided")
	}

	codeFilePaths := strings.Split(*codeFiles, ",")
	pkgName, concatenatedCode, err := ConcatFiles(codeFilePaths)
	if err != nil {
		fatalf("Failed to concatenate code files: %v", err)
	}

	apiClient, err := NewClient(*model, *maxTokens)
	if err != nil {
		fatalf("Failed to initialize OpenAI API client: %v", err)
	}
	if cases != nil && *cases {
		if whatToTest != nil && *whatToTest == "" {
			fatalf("Must provide what to test")
		}

		// spec, err := apiClient.GenerateSpec(*whatToTest, concatenatedCode, *extraInstructions)
		// if err != nil {
		// 	log.Fatalf("Failed to generate spec: %v", err)
		// }

		list, err := apiClient.GenerateTestsList(*whatToTest, concatenatedCode, *extraInstructions)
		if err != nil {
			fatalf("Failed to generate test list: %v", err)
		}

		s, err := apiClient.GenerateTestCases(*whatToTest, concatenatedCode, list, *extraInstructions)
		if err != nil {
			fatalf("Failed to generate test cases: %v", err)
		}
		s = "testing: " + *whatToTest + "\n" + removeYamlLines(s)
		err = WriteToFile(s, *specFilePath)
		if err != nil {
			fatalf("Failed to write test cases to file: %v", err)
		}
		fmt.Println("Done generating test cases")
		fmt.Printf("Test cases written to %s\n", *specFilePath)
		fmt.Println("Command to generate test code:")
		fmt.Print("goptest -spec-file=" + *specFilePath + " -code-files=" + *codeFiles + " -output-file=" + "generated_test.go")
		return
	}

	if *outputFilePath == "" {
		fatalf("Must provide output file path")
	}

	// TODO: Refine specs with mocks again - do multiple iterations
	specs, err := LoadTestSpecs(*specFilePath)
	if err != nil {
		fatalf("Failed to load test specs: %v", err)
	}
	// fmt.Println("Generating mocks code")
	// mocksCode, err := apiClient.GenerateMocks(specs.Testing, concatenatedCode, *extraInstructions)
	// if err != nil {
	// 	log.Fatalf("Failed to generate mocks code: %v", err)
	// }

	responses := make([]string, len(specs.Specs))
	var wg sync.WaitGroup
	max := make(chan struct{}, 2)
	for i, spec := range specs.Specs {
		max <- struct{}{}
		wg.Add(1)
		fmt.Printf("Generating test code %d of %d for spec '%s'\n", i+1, len(specs.Specs), spec.Description)
		go func(i int, spec Spec) {
			defer wg.Done()
			defer func() {
				<-max
			}()
			code, err := apiClient.GenerateTestCode(
				spec,
				specs.Testing,
				concatenatedCode,
				pkgName,
				*extraInstructions,
			)
			if err != nil {
				fatalf("Failed to generate test code for spec '%s': %v", spec.Description, err)
			}
			responses[i] = code
			fmt.Println("Done generating test")
		}(i, spec)
	}

	wg.Wait()

	// combinedCode := AggregateFiles(pkgName, append([]string{mocksCode}, responses...), true)
	combinedCode := AggregateFiles(pkgName, responses, true)

	err = WriteToFile(combinedCode, *outputFilePath)
	if err != nil {
		fatalf("Failed to write output to file: %v", err)
	}

	fmt.Println("Test generation succeeded. Check the output file for the generated test code.")
	fmt.Printf(*outputFilePath)
	fmt.Println()
}
