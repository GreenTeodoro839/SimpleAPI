package translate

// init registers all six directional translation pairs. Request converts the
// client (From) body to the upstream (To) body. Response and ResponseStream
// convert an upstream (To) response back to the client (From) format — so the
// stream transform name is the REVERSE of the pair's From→To direction.
func init() {
	// anthropic <-> openai_completion
	Register(&Pair{From: Anthropic, To: OpenAICompletion,
		Request:        func(b []byte) ([]byte, error) { return buildOpenAIRequest(parseAnthropicRequest(b)), nil },
		Response:       func(b []byte) ([]byte, error) { return buildAnthropicResponse(parseOpenAIResponse(b)), nil },
		ResponseStream: streamOpenAIToAnthropic,
	})
	Register(&Pair{From: OpenAICompletion, To: Anthropic,
		Request:        func(b []byte) ([]byte, error) { return buildAnthropicRequest(parseOpenAIRequest(b)), nil },
		Response:       func(b []byte) ([]byte, error) { return buildOpenAIResponse(parseAnthropicResponse(b)), nil },
		ResponseStream: streamAnthropicToOpenAI,
	})

	// anthropic <-> codex
	Register(&Pair{From: Anthropic, To: Codex,
		Request:        func(b []byte) ([]byte, error) { return buildCodexRequest(parseAnthropicRequest(b)), nil },
		Response:       func(b []byte) ([]byte, error) { return buildAnthropicResponse(parseCodexResponse(b)), nil },
		ResponseStream: streamCodexToAnthropic,
	})
	Register(&Pair{From: Codex, To: Anthropic,
		Request:        func(b []byte) ([]byte, error) { return buildAnthropicRequest(parseCodexRequest(b)), nil },
		Response:       func(b []byte) ([]byte, error) { return buildCodexResponse(parseAnthropicResponse(b)), nil },
		ResponseStream: streamAnthropicToCodex,
	})

	// openai_completion <-> codex
	Register(&Pair{From: OpenAICompletion, To: Codex,
		Request:        func(b []byte) ([]byte, error) { return buildCodexRequest(parseOpenAIRequest(b)), nil },
		Response:       func(b []byte) ([]byte, error) { return buildOpenAIResponse(parseCodexResponse(b)), nil },
		ResponseStream: streamCodexToOpenAI,
	})
	Register(&Pair{From: Codex, To: OpenAICompletion,
		Request:        func(b []byte) ([]byte, error) { return buildOpenAIRequest(parseCodexRequest(b)), nil },
		Response:       func(b []byte) ([]byte, error) { return buildCodexResponse(parseOpenAIResponse(b)), nil },
		ResponseStream: streamOpenAIToCodex,
	})
}
