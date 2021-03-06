package runtime

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/golang/glog"
	"github.com/golang/protobuf/proto"
	"golang.org/x/net/context"
	"google.golang.org/grpc"
)

type responseStreamChunk struct {
	Result proto.Message       `json:"result,omitempty"`
	Error  *responseStreamError `json:"error,omitempty"`
}

type responseStreamError struct {
	GrpcCode   int    `json:"grpc_code, omitempty"`
	HTTPCode   int    `json:"http_code, omitempty"`
	Message    string `json:"message, omitempty"`
	HTTPStatus string `json:"http_status, omitempty"`
}

// ForwardResponseStream forwards the stream from gRPC server to REST client.
func ForwardResponseStream(ctx context.Context, w http.ResponseWriter, req *http.Request, recv func() (proto.Message, error), opts ...func(context.Context, http.ResponseWriter, proto.Message) error) {
	f, ok := w.(http.Flusher)
	if !ok {
		glog.Errorf("Flush not supported in %T", w)
		http.Error(w, "unexpected type of web server", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Transfer-Encoding", "chunked")
	w.Header().Set("Content-Type", "application/json")
	if err := handleForwardResponseOptions(ctx, w, nil, opts); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	f.Flush()
	for {
		resp, err := recv()
		if err == io.EOF {
			return
		}
		if err != nil {
			handleForwardResponseStreamError(w, err)
			return
		}
		if err := handleForwardResponseOptions(ctx, w, resp, opts); err != nil {
			handleForwardResponseStreamError(w, err)
			return
		}
		buf, err := json.Marshal(responseStreamChunk{Result: resp})
		if err != nil {
			glog.Errorf("Failed to marshal response chunk: %v", err)
			return
		}
		if _, err = fmt.Fprintf(w, "%s\n", buf); err != nil {
			glog.Errorf("Failed to send response chunk: %v", err)
			return
		}
		f.Flush()
	}
}

// ForwardResponseMessage forwards the message "resp" from gRPC server to REST client.
func ForwardResponseMessage(ctx context.Context, w http.ResponseWriter, req *http.Request, resp proto.Message, opts ...func(context.Context, http.ResponseWriter, proto.Message) error) {
	w.Header().Set("Content-Type", "application/json")
	if err := handleForwardResponseOptions(ctx, w, resp, opts); err != nil {
		HTTPError(ctx, w, err)
		return
	}

	buf, err := json.Marshal(resp)
	if err != nil {
		glog.Errorf("Marshal error: %v", err)
		HTTPError(ctx, w, err)
		return
	}

	if _, err = w.Write(buf); err != nil {
		glog.Errorf("Failed to write response: %v", err)
	}
}

func handleForwardResponseOptions(ctx context.Context, w http.ResponseWriter, resp proto.Message, opts []func(context.Context, http.ResponseWriter, proto.Message) error) error {
	if len(opts) == 0 {
		return nil
	}
	for _, opt := range opts {
		if err := opt(ctx, w, resp); err != nil {
			glog.Errorf("Error handling ForwardResponseOptions: %v", err)
			return err
		}
	}
	return nil
}

func handleForwardResponseStreamError(w http.ResponseWriter, err error) {
	grpcCode := grpc.Code(err)
	httpCode := HTTPStatusFromCode(grpcCode)
	resp := responseStreamChunk{Error: &responseStreamError{GrpcCode: int(grpcCode),
		HTTPCode:   httpCode,
		Message:    err.Error(),
		HTTPStatus: http.StatusText(httpCode)}}
	buf, merr := json.Marshal(resp)
	if merr != nil {
		glog.Errorf("Failed to marshal an error: %v", merr)
		return
	}
	if _, werr := fmt.Fprintf(w, "%s\n", buf); werr != nil {
		glog.Errorf("Failed to notify error to client: %v", werr)
		return
	}
}
