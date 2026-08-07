// Copyright 2021 The Gitea Authors. All rights reserved.
// Copyright 2021 The nektos/act Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package model

type JobContext struct {
	Status    string                `json:"status"`
	Container JobContainerContext   `json:"container"`
	Services  map[string]JobService `json:"services"`
}

type JobContainerContext struct {
	ID      string `json:"id"`
	Network string `json:"network"`
}

type JobService struct {
	ID      string            `json:"id"`
	Network string            `json:"network"`
	Ports   map[string]string `json:"ports"` // container port to the published host port
}
