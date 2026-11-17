package model

import "time"

type Instance struct {
	Name           string    `json:"name"`
	IP             string    `json:"ip"`
	URL            string    `json:"url"`
	DeploymentType string    `json:"deployment_type,omitempty"`
	Executable     string    `json:"executable,omitempty"`
	AppPort        int       `json:"app_port,omitempty"`
	AppArgs        string    `json:"app_args,omitempty"`
	Status         string    `json:"status"`
	Message        string    `json:"message,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

const (
	StatusCreating = "Creando"
	StatusActive   = "Activo"
	StatusDeleting = "Eliminando"
	StatusError    = "Error"
)
