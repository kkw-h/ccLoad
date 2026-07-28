package app

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"

	"ccLoad/internal/model"

	"github.com/gin-gonic/gin"
)

func (s *Server) AdminListExternalAuthEnvironments(c *gin.Context) {
	items, err := s.store.ListExternalAuthEnvironments(c.Request.Context())
	if err != nil {
		log.Printf("[ERROR] list external auth environments: %v", err)
		RespondErrorMsg(c, http.StatusInternalServerError, "failed to list external auth environments")
		return
	}
	RespondJSON(c, http.StatusOK, items)
}

func (s *Server) AdminCreateExternalAuthEnvironment(c *gin.Context) {
	var input model.ExternalAuthEnvironment
	if err := c.ShouldBindJSON(&input); err != nil {
		RespondErrorMsg(c, http.StatusBadRequest, "invalid request")
		return
	}
	if err := s.validateExternalAuthEnvironment(c, &input); err != nil {
		RespondErrorMsg(c, http.StatusBadRequest, err.Error())
		return
	}
	created, err := s.store.CreateExternalAuthEnvironment(c.Request.Context(), &input)
	if errors.Is(err, model.ErrExternalAuthEnvironmentConflict) {
		RespondErrorMsg(c, http.StatusConflict, "external auth environment already exists")
		return
	}
	if err != nil {
		log.Printf("[ERROR] create external auth environment: %v", err)
		RespondErrorMsg(c, http.StatusInternalServerError, "failed to create external auth environment")
		return
	}
	if err := s.reloadExternalAuthEnvironments(c.Request.Context()); err != nil {
		RespondErrorMsg(c, http.StatusInternalServerError, "failed to activate external auth environment")
		return
	}
	RespondJSON(c, http.StatusCreated, created)
}

func (s *Server) AdminUpdateExternalAuthEnvironment(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		RespondErrorMsg(c, http.StatusBadRequest, "invalid environment id")
		return
	}
	var input model.ExternalAuthEnvironment
	if err := c.ShouldBindJSON(&input); err != nil {
		RespondErrorMsg(c, http.StatusBadRequest, "invalid request")
		return
	}
	input.ID = id
	if err := s.validateExternalAuthEnvironment(c, &input); err != nil {
		RespondErrorMsg(c, http.StatusBadRequest, err.Error())
		return
	}
	updated, err := s.store.UpdateExternalAuthEnvironment(c.Request.Context(), &input)
	switch {
	case errors.Is(err, model.ErrExternalAuthEnvironmentNotFound):
		RespondErrorMsg(c, http.StatusNotFound, "external auth environment not found")
		return
	case errors.Is(err, model.ErrExternalAuthEnvironmentConflict):
		RespondErrorMsg(c, http.StatusConflict, "external auth environment already exists")
		return
	case err != nil:
		log.Printf("[ERROR] update external auth environment: %v", err)
		RespondErrorMsg(c, http.StatusInternalServerError, "failed to update external auth environment")
		return
	}
	if err := s.reloadExternalAuthEnvironments(c.Request.Context()); err != nil {
		RespondErrorMsg(c, http.StatusInternalServerError, "failed to activate external auth environment")
		return
	}
	RespondJSON(c, http.StatusOK, updated)
}

func (s *Server) AdminDeleteExternalAuthEnvironment(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		RespondErrorMsg(c, http.StatusBadRequest, "invalid environment id")
		return
	}
	err = s.store.DeleteExternalAuthEnvironment(c.Request.Context(), id)
	if errors.Is(err, model.ErrExternalAuthEnvironmentNotFound) {
		RespondErrorMsg(c, http.StatusNotFound, "external auth environment not found")
		return
	}
	if err != nil {
		log.Printf("[ERROR] delete external auth environment: %v", err)
		RespondErrorMsg(c, http.StatusInternalServerError, "failed to delete external auth environment")
		return
	}
	if err := s.reloadExternalAuthEnvironments(c.Request.Context()); err != nil {
		RespondErrorMsg(c, http.StatusInternalServerError, "failed to refresh external auth environments")
		return
	}
	RespondJSON(c, http.StatusOK, gin.H{"id": id})
}

func (s *Server) validateExternalAuthEnvironment(c *gin.Context, input *model.ExternalAuthEnvironment) error {
	environment, err := model.NormalizeExternalAuthEnvironment(input.Environment)
	if err != nil {
		return err
	}
	resolver := s.externalAuthResolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	if err := validateExternalAuthEndpoint(c.Request.Context(), input.AuthzURL, resolver); err != nil {
		return err
	}
	input.Environment = environment
	return nil
}

func (s *Server) reloadExternalAuthEnvironments(ctx context.Context) error {
	items, err := s.store.ListExternalAuthEnvironments(ctx)
	if err != nil {
		return err
	}
	targets, err := buildExternalAuthEnvironmentTargets(items)
	if err != nil {
		return err
	}
	if s.externalAuthService != nil {
		s.externalAuthService.ReplaceEnvironments(targets)
	}
	return nil
}

func buildExternalAuthEnvironmentTargets(items []*model.ExternalAuthEnvironment) (map[string]externalAuthEnvironmentTarget, error) {
	targets := make(map[string]externalAuthEnvironmentTarget)
	for _, item := range items {
		if item == nil || !item.IsActive {
			continue
		}
		parsed, err := url.Parse(item.AuthzURL)
		if err != nil {
			return nil, err
		}
		targets[item.Environment] = externalAuthEnvironmentTarget{Environment: item.Environment, AuthzURL: parsed}
	}
	return targets, nil
}
