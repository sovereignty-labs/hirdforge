package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	a2apb "git.hirdforge.com/kit/hirdforge/proto/a2a"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
)

type a2aGRPCServer struct {
	a2apb.UnimplementedA2AServiceServer
	runtime *a2aRuntime
}

func startA2AGRPCServer(port string, runtime *a2aRuntime) error {
	lis, err := net.Listen("tcp", ":"+strings.TrimSpace(port))
	if err != nil {
		return err
	}
	server := grpc.NewServer()
	a2apb.RegisterA2AServiceServer(server, &a2aGRPCServer{runtime: runtime})
	return server.Serve(lis)
}

func (s *a2aGRPCServer) SendMessage(ctx context.Context, req *a2apb.SendMessageRequest) (*a2apb.Task, error) {
	task, _, err := s.runtime.submit(a2aRequestFromProto(req), nil)
	if err != nil {
		return nil, grpcErrorFor(err)
	}
	return protoTaskFromLocal(task)
}

func (s *a2aGRPCServer) SendStreamingMessage(req *a2apb.SendMessageRequest, stream a2apb.A2AService_SendStreamingMessageServer) error {
	events := make(chan a2aRuntimeEvent, 32)
	task, done, err := s.runtime.submit(a2aRequestFromProto(req), func(evt a2aRuntimeEvent) bool {
		select {
		case events <- evt:
			return true
		case <-stream.Context().Done():
			return false
		}
	})
	if err != nil {
		return grpcErrorFor(err)
	}
	submitted := &a2aTaskStatusUpdateEvent{
		TaskID: task.ID,
		Status: task.Status,
		Final:  false,
	}
	if err := stream.Send(&a2apb.TaskEvent{
		Payload: &a2apb.TaskEvent_StatusUpdate{
			StatusUpdate: protoStatusUpdateFromLocal(submitted),
		},
	}); err != nil {
		return err
	}
	go func() {
		<-done
		close(events)
	}()
	for evt := range events {
		if evt.Status != nil {
			if err := stream.Send(&a2apb.TaskEvent{
				Payload: &a2apb.TaskEvent_StatusUpdate{
					StatusUpdate: protoStatusUpdateFromLocal(evt.Status),
				},
			}); err != nil {
				return err
			}
		}
		if evt.Artifact != nil {
			if err := stream.Send(&a2apb.TaskEvent{
				Payload: &a2apb.TaskEvent_ArtifactUpdate{
					ArtifactUpdate: protoArtifactUpdateFromLocal(evt.Artifact),
				},
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *a2aGRPCServer) GetTask(ctx context.Context, req *a2apb.GetTaskRequest) (*a2apb.Task, error) {
	task, err := s.runtime.getTask(req.GetId())
	if err != nil {
		return nil, grpcErrorFor(err)
	}
	return protoTaskFromLocal(task)
}

func (s *a2aGRPCServer) CancelTask(ctx context.Context, req *a2apb.CancelTaskRequest) (*a2apb.Task, error) {
	task, err := s.runtime.cancelTask(req.GetId())
	if err != nil {
		return nil, grpcErrorFor(err)
	}
	return protoTaskFromLocal(task)
}

func grpcErrorFor(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errA2ABusy):
		return status.Error(codes.ResourceExhausted, err.Error())
	case errors.Is(err, errA2ANotFound):
		return status.Error(codes.NotFound, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}

func a2aRequestFromProto(req *a2apb.SendMessageRequest) a2aSendMessageRequest {
	if req == nil {
		return a2aSendMessageRequest{}
	}
	out := a2aSendMessageRequest{
		Message: a2aMessageFromProto(req.GetMessage()),
	}
	if cfg := req.GetPushNotification(); cfg != nil {
		out.PushNotification = &a2aPushNotificationConfig{
			URL:   strings.TrimSpace(cfg.GetUrl()),
			Token: strings.TrimSpace(cfg.GetToken()),
		}
	}
	return out
}

func protoTaskFromLocal(task *a2aTask) (*a2apb.Task, error) {
	if task == nil {
		return nil, fmt.Errorf("task is nil")
	}
	metadata, err := structpb.NewStruct(cloneAnyMap(task.Metadata))
	if err != nil {
		return nil, err
	}
	out := &a2apb.Task{
		Id:        task.ID,
		ContextId: task.ContextID,
		Status:    protoStatusFromLocal(task.Status),
		Metadata:  metadata,
	}
	for _, artifact := range task.Artifacts {
		out.Artifacts = append(out.Artifacts, protoArtifactFromLocal(artifact))
	}
	return out, nil
}

func protoStatusFromLocal(statusValue a2aTaskStatus) *a2apb.TaskStatus {
	return &a2apb.TaskStatus{
		State:     protoTaskStateFromLocal(statusValue.State),
		Timestamp: statusValue.Timestamp,
		Message:   protoMessageFromLocal(statusValue.Message),
	}
}

func protoTaskStateFromLocal(state a2aTaskState) a2apb.TaskState {
	switch state {
	case a2aTaskStateSubmitted:
		return a2apb.TaskState_TASK_STATE_SUBMITTED
	case a2aTaskStateWorking:
		return a2apb.TaskState_TASK_STATE_WORKING
	case a2aTaskStateCompleted:
		return a2apb.TaskState_TASK_STATE_COMPLETED
	case a2aTaskStateFailed:
		return a2apb.TaskState_TASK_STATE_FAILED
	case a2aTaskStateCanceled:
		return a2apb.TaskState_TASK_STATE_CANCELED
	case a2aTaskStateInputNeeded:
		return a2apb.TaskState_TASK_STATE_INPUT_NEEDED
	default:
		return a2apb.TaskState_TASK_STATE_UNSPECIFIED
	}
}

func protoMessageFromLocal(msg *a2aMessage) *a2apb.Message {
	if msg == nil {
		return nil
	}
	out := &a2apb.Message{
		Role:      msg.Role,
		MessageId: msg.MessageID,
	}
	for _, part := range msg.Parts {
		out.Parts = append(out.Parts, protoPartFromLocal(part))
	}
	return out
}

func protoPartFromLocal(part a2aPart) *a2apb.Part {
	out := &a2apb.Part{
		Text: part.Text,
	}
	if part.Data != nil {
		if value, err := structpb.NewValue(part.Data); err == nil {
			out.Data = value
		}
	}
	if metadata, err := structpb.NewStruct(cloneAnyMap(part.Metadata)); err == nil {
		out.Metadata = metadata
	}
	return out
}

func protoArtifactFromLocal(artifact a2aArtifact) *a2apb.Artifact {
	out := &a2apb.Artifact{ArtifactId: artifact.ArtifactID}
	for _, part := range artifact.Parts {
		out.Parts = append(out.Parts, protoPartFromLocal(part))
	}
	return out
}

func protoStatusUpdateFromLocal(update *a2aTaskStatusUpdateEvent) *a2apb.TaskStatusUpdateEvent {
	if update == nil {
		return nil
	}
	return &a2apb.TaskStatusUpdateEvent{
		TaskId: update.TaskID,
		Status: protoStatusFromLocal(update.Status),
		Final:  update.Final,
	}
}

func protoArtifactUpdateFromLocal(update *a2aTaskArtifactUpdateEvent) *a2apb.TaskArtifactUpdateEvent {
	if update == nil {
		return nil
	}
	return &a2apb.TaskArtifactUpdateEvent{
		TaskId:   update.TaskID,
		Artifact: protoArtifactFromLocal(update.Artifact),
	}
}

func a2aMessageFromProto(msg *a2apb.Message) a2aMessage {
	if msg == nil {
		return a2aMessage{}
	}
	out := a2aMessage{
		Role:      strings.TrimSpace(msg.GetRole()),
		MessageID: strings.TrimSpace(msg.GetMessageId()),
	}
	for _, part := range msg.GetParts() {
		out.Parts = append(out.Parts, a2aPartFromProto(part))
	}
	return out
}

func a2aPartFromProto(part *a2apb.Part) a2aPart {
	if part == nil {
		return a2aPart{}
	}
	out := a2aPart{
		Text: strings.TrimSpace(part.GetText()),
	}
	if part.GetData() != nil {
		out.Data = part.GetData().AsInterface()
	}
	if part.GetMetadata() != nil {
		out.Metadata = part.GetMetadata().AsMap()
	}
	return out
}
