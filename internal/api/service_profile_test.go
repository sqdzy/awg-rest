package api

import (
	"testing"

	"github.com/awg-rest/awg-rest/internal/domain"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestValidateRequestedProfile_InheritsWhenSelectorsOmitted(t *testing.T) {
	t.Parallel()
	p := domain.ProtocolProfile{ID: uuid.New(), Name: "default-v2"}
	require.NoError(t, validateRequestedProfile(p, CreatePeerRequest{}))
}

func TestValidateRequestedProfile_AcceptsMatchingSelectors(t *testing.T) {
	t.Parallel()
	p := domain.ProtocolProfile{ID: uuid.New(), Name: "default-v2"}
	id := p.ID.String()
	name := p.Name
	require.NoError(t, validateRequestedProfile(p, CreatePeerRequest{
		ProfileID: &id, ProfileName: &name,
	}))
}

func TestValidateRequestedProfile_RejectsMismatchedID(t *testing.T) {
	t.Parallel()
	p := domain.ProtocolProfile{ID: uuid.New(), Name: "default-v2"}
	other := uuid.New().String()
	err := validateRequestedProfile(p, CreatePeerRequest{ProfileID: &other})
	require.Error(t, err)
	ve, ok := err.(domain.ValidationErrors)
	require.True(t, ok)
	require.Equal(t, "node_profile_mismatch", ve[0].Code)
}

func TestValidateRequestedProfile_RejectsMismatchedName(t *testing.T) {
	t.Parallel()
	p := domain.ProtocolProfile{ID: uuid.New(), Name: "default-v2"}
	other := "other-profile"
	err := validateRequestedProfile(p, CreatePeerRequest{ProfileName: &other})
	require.Error(t, err)
	ve, ok := err.(domain.ValidationErrors)
	require.True(t, ok)
	require.Equal(t, "node_profile_mismatch", ve[0].Code)
}

func TestValidateRequestedProfile_RejectsInvalidID(t *testing.T) {
	t.Parallel()
	p := domain.ProtocolProfile{ID: uuid.New(), Name: "default-v2"}
	bad := "not-a-uuid"
	err := validateRequestedProfile(p, CreatePeerRequest{ProfileID: &bad})
	require.Error(t, err)
	require.Contains(t, err.Error(), "profile_id")
}
