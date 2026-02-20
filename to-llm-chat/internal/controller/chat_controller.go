/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"os"

	"github.com/openai/openai-go/v3/option"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/openai/openai-go/v3"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	comsalaboyv1 "github.com/salaboy/the-worst-way/to-llm-chat/api/v1"
)

// Definitions to manage status conditions
const (
	// typeAvailableChat represents the status of the Chat reconciliation
	typeAvailableChat = "Available"
	// typeProgressingChat represents the status used when the Chat is being reconciled
	typeProgressingChat = "Progressing"
	// typeDegradedChat represents the status used when the Chat has encountered an error
	typeDegradedChat = "Degraded"
	// typeHallucinatingChat represents the status used when the Chat has gone mad
	typeHallucinatingChat = "Hallucinating"
)

// ChatReconciler reconciles a Chat object
type ChatReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=com.salaboy.salaboy.com,resources=chats,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=com.salaboy.salaboy.com,resources=chats/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=com.salaboy.salaboy.com,resources=chats/finalizers,verbs=update
// +kubebuilder:rbac:groups=core,resources=events,verbs=create;patch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Chat object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.22.4/pkg/reconcile
func (r *ChatReconciler) Reconcile(ctx context.Context,
	req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var chat comsalaboyv1.Chat
	//Get the resource fresh from the API server
	if err := r.Get(ctx, req.NamespacedName, &chat); err != nil {
		if apierrors.IsNotFound(err) {
			// If the custom resource is not found then it usually means that it was deleted or not created
			// In this way, we will stop the reconciliation
			log.Info("Chat resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue the request.
		log.Error(err, "Failed to get Chat")
		return ctrl.Result{}, err
	}

	// name of our custom finalizer
	byeByeLLMFinalizerName := "chat.salaboy.com/finalizer"

	// examine DeletionTimestamp to determine if object is under deletion
	if chat.ObjectMeta.DeletionTimestamp.IsZero() {
		// The object is not being deleted, so if it does not have our finalizer,
		// then let's add the finalizer and update the object. This is equivalent
		// to registering our finalizer.
		if !controllerutil.ContainsFinalizer(&chat, byeByeLLMFinalizerName) {
			controllerutil.AddFinalizer(&chat, byeByeLLMFinalizerName)
			if err := r.Update(ctx, &chat); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, nil
		}
	} else {
		if controllerutil.ContainsFinalizer(&chat, byeByeLLMFinalizerName) {
			// our finalizer is present, so let's handle any external dependency

			log.Info("Bye Bye LLM, we will miss you!")

			// remove our finalizer from the list and update it.
			controllerutil.RemoveFinalizer(&chat, byeByeLLMFinalizerName)
			if err := r.Update(ctx, &chat); err != nil {
				return ctrl.Result{}, err
			}
		}
		// Stop reconciliation as the item is being deleted
		return ctrl.Result{}, nil
	}

	// Initialize status conditions if not yet present
	if len(chat.Status.Conditions) == 0 {
		meta.SetStatusCondition(&chat.Status.Conditions, metav1.Condition{
			Type:               typeProgressingChat,
			Status:             metav1.ConditionUnknown,
			Reason:             "Reconciling",
			Message:            "Starting reconciliation",
			LastTransitionTime: metav1.Now(),
			ObservedGeneration: chat.Generation,
		})

		if err := r.Status().Update(ctx, &chat); err != nil {
			log.Error(err, "Failed to update Chat status")
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Call LLM with the chat message
	openAIKey, _ := os.LookupEnv("OPENAI_API_KEY")

	client := openai.NewClient(
		option.WithAPIKey(openAIKey), // defaults to os.LookupEnv("OPENAI_API_KEY")
	)

	chatCompleted := false
	for _, condition := range chat.Status.Conditions {
		if condition.Reason == "ChatCompleted" &&
			condition.ObservedGeneration == chat.Generation &&
			condition.Status == metav1.ConditionTrue {
			chatCompleted = true
			break
		}
	}

	if !chatCompleted {
		prompt := *chat.Spec.Question

		log.Info(">> Calling LLM with prompt:", "prompt", prompt)

		chatCompletion, err := client.Chat.Completions.New(context.TODO(), openai.ChatCompletionNewParams{
			Messages: []openai.ChatCompletionMessageParamUnion{
				openai.UserMessage(prompt),
			},
			Model: openai.ChatModelGPT4o,
		})

		if err != nil {
			// Re-fetch the Chat after updating the status
			if err := r.Get(ctx, req.NamespacedName, &chat); err != nil {
				log.Error(err, "Failed to re-fetch Chat")
				return ctrl.Result{}, err
			}

			meta.SetStatusCondition(&chat.Status.Conditions, metav1.Condition{
				Type:               typeDegradedChat,
				Status:             metav1.ConditionTrue,
				Reason:             "ErrorCallingLLM",
				Message:            err.Error(),
				LastTransitionTime: metav1.Now(),
				ObservedGeneration: chat.Generation,
			})
			if statusErr := r.Status().Update(ctx, &chat); statusErr != nil {
				log.Error(statusErr, "Failed to update Chat status")
			}
			return ctrl.Result{}, err
		}
		log.Info(">> LLM response:", "response", chatCompletion.Choices[0].Message.Content)

		question := *chat.Spec.Question
		r.Recorder.Eventf(&chat, corev1.EventTypeNormal, "Updated",
			"%s - %s \n > Question: %s \nResponse From LLM: %s",
			chat.Name,
			chat.Namespace,
			question,
			chatCompletion.Choices[0].Message.Content[0:15])

		// Re-fetch the Chat before updating the status
		if err := r.Get(ctx, req.NamespacedName, &chat); err != nil {
			log.Error(err, "Failed to re-fetch Chat")
			return ctrl.Result{}, err
		}

		meta.SetStatusCondition(&chat.Status.Conditions, metav1.Condition{
			Type:               typeAvailableChat,
			Status:             metav1.ConditionTrue,
			Reason:             "ChatCompleted",
			Message:            chatCompletion.Choices[0].Message.Content[0:15],
			LastTransitionTime: metav1.Now(),
			ObservedGeneration: chat.Generation,
		})

		if statusErr := r.Status().Update(ctx, &chat); statusErr != nil {
			log.Error(statusErr, "Failed to update Chat status")
		}
	}

	return ctrl.Result{}, nil

}

// SetupWithManager sets up the controller with the Manager.
func (r *ChatReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&comsalaboyv1.Chat{}).
		Named("chat").
		Complete(r)
}
