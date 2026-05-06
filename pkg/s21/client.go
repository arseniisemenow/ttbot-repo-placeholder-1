package s21

import (
	"context"
	"errors"
	"fmt"
	"strings"

	s21client "github.com/arseniisemenow/s21auto-client-go"
	"github.com/arseniisemenow/s21auto-client-go/requests"
)

// realClient is the production Client backed by s21auto-client-go.
//
// Each call instantiates a fresh client with the given credentials. This is
// simple and stateless — refresh tokens live inside the auth provider per
// call, which is fine for our low call volume.
type realClient struct{}

// NewClient returns a Client that talks to platform.21-school.ru.
func NewClient() Client { return realClient{} }

func (realClient) Authenticate(ctx context.Context, login, password string) (Profile, error) {
	c := s21client.New(s21client.DefaultAuth(login, password))
	data, err := c.R().DashboardHeaderGetInfo(requests.DashboardHeaderGetInfo_Variables{})
	if err != nil {
		return Profile{}, mapAuthError(err)
	}
	return profileFromDashboardData(data, login)
}

func (realClient) LookupByLogin(ctx context.Context, adminLogin, adminPassword, targetLogin string) (Profile, error) {
	c := s21client.New(s21client.DefaultAuth(adminLogin, adminPassword))
	// Verify the admin's token first.
	if _, err := c.R().DashboardHeaderGetInfo(requests.DashboardHeaderGetInfo_Variables{}); err != nil {
		return Profile{}, mapAuthError(err)
	}
	// Resolve the target login.
	res, err := c.R().PublicProfileGetCredentialsByLogin(requests.PublicProfileGetCredentialsByLogin_Variables{
		Login: targetLogin,
	})
	if err != nil {
		return Profile{}, fmt.Errorf("s21 lookup: %w", err)
	}
	student := res.School21.GetStudentByLogin
	if student.StudentID == "" {
		return Profile{}, ErrNotFound
	}
	// PublicProfileGetCredentialsByLogin doesn't return campus directly; we fall
	// back to the admin's own campus as a reasonable assumption for MVP. (The
	// docs note that campus discovery for *other* users via public profile is
	// limited; full implementation would query a richer endpoint.)
	adminProfile, err := profileFromDashboardOrLogin(c, adminLogin)
	if err != nil {
		return Profile{}, err
	}
	return Profile{
		Login:         targetLogin,
		CampusID:      adminProfile.CampusID,
		CampusName:    adminProfile.CampusName,
		CoalitionName: "",
	}, nil
}

func profileFromDashboardOrLogin(c *s21client.Client, login string) (Profile, error) {
	data, err := c.R().DashboardHeaderGetInfo(requests.DashboardHeaderGetInfo_Variables{})
	if err != nil {
		return Profile{}, mapAuthError(err)
	}
	return profileFromDashboardData(data, login)
}

func profileFromDashboardData(data requests.DashboardHeaderGetInfo_Data, login string) (Profile, error) {
	user := data.User.GetCurrentUser
	if user.Login == "" {
		return Profile{}, ErrInvalidCredentials
	}
	var campusID, campusName string
	if len(user.StudentRoles) > 0 {
		campusID = user.StudentRoles[0].School.ID
		campusName = user.StudentRoles[0].School.ShortName
	}
	coalition := data.Student.GetUserTournamentWidget.CoalitionMember.Coalition.Name
	return Profile{
		Login:         user.Login,
		CampusID:      campusID,
		CampusName:    campusName,
		CoalitionName: coalition,
	}, nil
}

func mapAuthError(err error) error {
	if err == nil {
		return nil
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "unauthorized"),
		strings.Contains(msg, "authentication failed"),
		strings.Contains(msg, "invalid"),
		strings.Contains(msg, "401"):
		return errors.Join(ErrInvalidCredentials, err)
	}
	return errors.Join(ErrUnavailable, err)
}
