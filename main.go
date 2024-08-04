package main

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"

	"encoding/json"

	admission "k8s.io/api/admission/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
)

type admissionValidationHandler struct {
	schemeDecoder runtime.Decoder
}

func (avh admissionValidationHandler) decodeRequest(request []byte, expectedGVK schema.GroupVersionKind, into runtime.Object) (schema.GroupVersionKind, error) {
	_, requestGVK, err := avh.schemeDecoder.Decode(request, nil, into)
	if err != nil {
		return schema.GroupVersionKind{}, err
	}
	if requestGVK == nil {
		return schema.GroupVersionKind{}, errors.New("unable to find schema group, version and kind from request")
	}

	if *requestGVK != expectedGVK {
		errMsg := fmt.Sprintf(`Expected admission review with group: %s version: %s kind: %s 
but got group: %s version: %s kind: %s`, expectedGVK.Group, expectedGVK.Version, expectedGVK.Kind, requestGVK.Group, requestGVK.Version, requestGVK.Kind)
		return schema.GroupVersionKind{}, errors.New(errMsg)
	}
	return *requestGVK, nil
}

func (avh admissionValidationHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	requestBody, err := io.ReadAll(r.Body)
	if err != nil {
		errMsg := fmt.Sprintf("error %s reading request body", err)
		slog.Error(errMsg)
		http.Error(w, errMsg, http.StatusInternalServerError)
		return
	}
	slog.Debug("request body read successfully", "request body", requestBody)
	admissionReview := new(admission.AdmissionReview)
	expectedAdmissionReviewGVK := schema.GroupVersionKind{
		Group:   "admission.k8s.io",
		Version: "v1",
		Kind:    "AdmissionReview",
	}
	admissionReviewGVK, err := avh.decodeRequest(requestBody, expectedAdmissionReviewGVK, admissionReview)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	slog.Info("Successfully decoded AdmissionReview")

	admissionReviewRequest := admissionReview.Request
	if admissionReviewRequest == nil {
		errMsg := "Expected admission review request but did not get one"
		slog.Error(errMsg)
		http.Error(w, errMsg, http.StatusInternalServerError)
		return
	}
	deploymentGVR := metav1.GroupVersionResource{
		Group:    "apps",
		Version:  "v1",
		Resource: "deployments",
	}
	if admissionReviewRequest.Resource != deploymentGVR {
		errMsg := fmt.Sprintf("Expected apps/v1/deployments resource but got %+v", admissionReviewRequest)
		slog.Error(errMsg)
		http.Error(w, errMsg, http.StatusBadRequest)
		return
	}

	deploymentRequest := new(appsv1.Deployment)
	expectedDeploymentGVK := schema.GroupVersionKind{
		Group:   "apps",
		Version: "v1",
		Kind:    "Deployment",
	}
	if _, err = avh.decodeRequest(admissionReviewRequest.Object.Raw, expectedDeploymentGVK, deploymentRequest); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	slog.Debug("Deployment request decoded successfully", "decoded request", deploymentRequest)
	var errorMessage string
	for _, container := range deploymentRequest.Spec.Template.Spec.Containers {
		if _, ok := container.Resources.Requests[corev1.ResourceMemory]; !ok {
			errorMessage = fmt.Sprintf("Memory request not specified for container %s", container.Name)
			break
		}
		if _, ok := container.Resources.Limits[corev1.ResourceMemory]; !ok {
			errorMessage = fmt.Sprintf("Memory limit not specified for container %s", container.Name)
			break
		}
	}
	admissionReviewResponse := &admission.AdmissionReview{
		Response: &admission.AdmissionResponse{
			UID: admissionReviewRequest.UID,
		},
	}
	admissionReviewResponse.SetGroupVersionKind(admissionReviewGVK)
	if errorMessage != "" {
		admissionReviewResponse.Response.Allowed = false
		admissionReviewResponse.Response.Result = &metav1.Status{
			Status:  "Failure",
			Message: errorMessage,
		}
	} else {
		admissionReviewResponse.Response.Allowed = true
	}
	respBytes, err := json.Marshal(admissionReviewResponse)
	if err != nil {
		slog.Error("error marshaling response for admission review", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	slog.Info("admission review response json marshalled", "json", respBytes)
	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write(respBytes); err != nil {
		slog.Error("error %s writing admission response", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func main() {
	runtimeScheme := runtime.NewScheme()
	if err := admission.AddToScheme(runtimeScheme); err != nil {
		slog.Error("error adding AdmissionReview to scheme", "error", err)
		os.Exit(1)
	}
	codecFactory := serializer.NewCodecFactory(runtimeScheme)
	deserializer := codecFactory.UniversalDeserializer()
	admissionValidationHandler := admissionValidationHandler{
		schemeDecoder: deserializer,
	}

	http.Handle("/validate", admissionValidationHandler)
	slog.Info("Server started ...")
	if err := http.ListenAndServeTLS(":7443", "/etc/ssl/certs/tls.crt", "/etc/ssl/certs/tls.key", nil); err != nil {
		slog.Error("error starting admission webhook", "error", err)
		os.Exit(1)
	}
}
