package jsend

type Envelope map[string]any

func Success(data any) Envelope {
	return Envelope{
		"status": "success",
		"data":   data,
	}
}

func Fail(data any) Envelope {
	return Envelope{
		"status": "fail",
		"data":   data,
	}
}

func Error(message string, data any) Envelope {
	payload := Envelope{
		"status":  "error",
		"message": message,
	}
	if data != nil {
		payload["data"] = data
	}
	return payload
}
