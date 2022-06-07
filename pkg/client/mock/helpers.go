// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

package mock

import "github.com/gofrs/uuid"

// NewID is a small wrapper that returns a UUID used for agent client IDs
func NewID() string {
	return uuid.Must(uuid.NewV4()).String()
}
