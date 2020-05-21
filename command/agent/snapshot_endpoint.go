package agent

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/hashicorp/go-msgpack/codec"
	"github.com/hashicorp/nomad/nomad/structs"
)

func (s *HTTPServer) SnapshotRequest(resp http.ResponseWriter, req *http.Request) (interface{}, error) {
	switch req.Method {
	case "GET":
		return s.snapshotSaveRequest(resp, req)
	default:
		return nil, CodedError(405, ErrInvalidMethod)
	}

}

func (s *HTTPServer) snapshotSaveRequest(resp http.ResponseWriter, req *http.Request) (interface{}, error) {
	args := &structs.SnapshotSaveRequest{}
	if s.parse(resp, req, &args.Region, &args.QueryOptions) {
		return nil, nil
	}

	var handler structs.StreamingRpcHandler
	var handlerErr error

	if server := s.agent.Server(); server != nil {
		handler, handlerErr = server.StreamingRpcHandler("Operator.SnapshotSave")
	} else if client := s.agent.Client(); client != nil {
		handler, handlerErr = client.RemoteStreamingRpcHandler("Operator.SnapshotSave")
	} else {
		handlerErr = fmt.Errorf("misconfigured connection")
	}

	if handlerErr != nil {
		return nil, CodedError(500, handlerErr.Error())
	}

	httpPipe, handlerPipe := net.Pipe()
	decoder := codec.NewDecoder(httpPipe, structs.MsgpackHandle)
	encoder := codec.NewEncoder(httpPipe, structs.MsgpackHandle)

	// Create a goroutine that closes the pipe if the connection closes.
	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()
	go func() {
		<-ctx.Done()
		httpPipe.Close()
	}()

	errCh := make(chan HTTPCodedError, 1)
	go func() {
		defer cancel()

		// Send the request
		if err := encoder.Encode(args); err != nil {
			errCh <- CodedError(500, err.Error())
			return
		}

		var res structs.SnapshotSaveResponse
		if err := decoder.Decode(&res); err != nil {
			errCh <- CodedError(500, err.Error())
			return
		}

		if res.ErrorMsg != "" {
			errCh <- CodedError(res.ErrorCode, res.ErrorMsg)
			return
		}

		resp.Header().Add("Digest", res.SnapshotChecksum)

		_, err := io.Copy(resp, httpPipe)
		if err != nil &&
			err != io.EOF &&
			!strings.Contains(err.Error(), "closed") &&
			!strings.Contains(err.Error(), "EOF") {
			errCh <- CodedError(500, err.Error())
			return
		}

		errCh <- nil
	}()

	handler(handlerPipe)
	cancel()
	codedErr := <-errCh

	return nil, codedErr
}
