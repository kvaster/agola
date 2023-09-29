// Copyright 2019 Sorint.lab
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied
// See the License for the specific language governing permissions and
// limitations under the License.

package action

import (
	"context"
	"time"

	"github.com/gofrs/uuid"
	"github.com/sorintlab/errors"

	"agola.io/agola/internal/services/configstore/db"
	"agola.io/agola/internal/sqlg/sql"
	"agola.io/agola/internal/util"
	"agola.io/agola/services/configstore/types"
)

type GetUsersRequest struct {
	StartUserName string

	Limit         int
	SortDirection types.SortDirection
}

type GetUsersResponse struct {
	Users []*types.User

	HasMore bool
}

func (h *ActionHandler) GetUsers(ctx context.Context, req *GetUsersRequest) (*GetUsersResponse, error) {
	limit := req.Limit
	if limit > 0 {
		limit += 1
	}

	var users []*types.User
	err := h.d.Do(ctx, func(tx *sql.Tx) error {
		var err error
		users, err = h.d.GetUsers(tx, req.StartUserName, limit, req.SortDirection)
		return errors.WithStack(err)
	})
	if err != nil {
		return nil, errors.WithStack(err)
	}

	var hasMore bool
	if req.Limit > 0 {
		hasMore = len(users) > req.Limit
		if hasMore {
			users = users[0:req.Limit]
		}
	}

	return &GetUsersResponse{
		Users:   users,
		HasMore: hasMore,
	}, nil
}

type CreateUserRequest struct {
	UserName string

	CreateUserLARequest *CreateUserLARequest
}

func (h *ActionHandler) CreateUser(ctx context.Context, req *CreateUserRequest) (*types.User, error) {
	if req.UserName == "" {
		return nil, util.NewAPIError(util.ErrBadRequest, errors.Errorf("user name required"))
	}
	if !util.ValidateName(req.UserName) {
		return nil, util.NewAPIError(util.ErrBadRequest, errors.Errorf("invalid user name %q", req.UserName))
	}

	var user *types.User
	err := h.d.Do(ctx, func(tx *sql.Tx) error {
		var err error

		// check duplicate user name
		u, err := h.d.GetUserByName(tx, req.UserName)
		if err != nil {
			return errors.WithStack(err)
		}
		if u != nil {
			return util.NewAPIError(util.ErrBadRequest, errors.Errorf("user with name %q already exists", u.Name))
		}

		var rs *types.RemoteSource
		if req.CreateUserLARequest != nil {
			rs, err = h.d.GetRemoteSourceByName(tx, req.CreateUserLARequest.RemoteSourceName)
			if err != nil {
				return errors.WithStack(err)
			}
			if rs == nil {
				return util.NewAPIError(util.ErrBadRequest, errors.Errorf("remote source %q doesn't exist", req.CreateUserLARequest.RemoteSourceName))
			}
			la, err := h.d.GetLinkedAccountByRemoteUserIDandSource(tx, req.CreateUserLARequest.RemoteUserID, rs.ID)
			if err != nil {
				return errors.Wrapf(err, "failed to get linked account for remote user id %q and remote source %q", req.CreateUserLARequest.RemoteUserID, rs.ID)
			}
			if la != nil {
				return util.NewAPIError(util.ErrBadRequest, errors.Errorf("linked account for remote user id %q for remote source %q already exists", req.CreateUserLARequest.RemoteUserID, req.CreateUserLARequest.RemoteSourceName))
			}
		}

		user = types.NewUser(tx)
		user.Name = req.UserName
		user.Secret = util.EncodeSha1Hex(uuid.Must(uuid.NewV4()).String())

		if err := h.d.InsertUser(tx, user); err != nil {
			return errors.WithStack(err)
		}

		// create root user project group
		pg := types.NewProjectGroup(tx)
		// use public visibility
		pg.Visibility = types.VisibilityPublic
		pg.Parent = types.Parent{
			Kind: types.ObjectKindUser,
			ID:   user.ID,
		}

		if err := h.d.InsertProjectGroup(tx, pg); err != nil {
			return errors.WithStack(err)
		}

		if req.CreateUserLARequest != nil {
			la := types.NewLinkedAccount(tx)
			la.UserID = user.ID
			la.RemoteSourceID = rs.ID
			la.RemoteUserID = req.CreateUserLARequest.RemoteUserID
			la.RemoteUserName = req.CreateUserLARequest.RemoteUserName
			la.UserAccessToken = req.CreateUserLARequest.UserAccessToken
			la.Oauth2AccessToken = req.CreateUserLARequest.Oauth2AccessToken
			la.Oauth2RefreshToken = req.CreateUserLARequest.Oauth2RefreshToken
			la.Oauth2AccessTokenExpiresAt = req.CreateUserLARequest.Oauth2AccessTokenExpiresAt

			if err := h.d.InsertLinkedAccount(tx, la); err != nil {
				return errors.WithStack(err)
			}
		}

		return nil
	})
	if err != nil {
		return nil, errors.WithStack(err)
	}

	return user, nil
}

func (h *ActionHandler) DeleteUser(ctx context.Context, userRef string) error {
	err := h.d.Do(ctx, func(tx *sql.Tx) error {
		var err error

		// check user existance
		user, err := h.d.GetUser(tx, userRef)
		if err != nil {
			return errors.WithStack(err)
		}
		if user == nil {
			return util.NewAPIError(util.ErrBadRequest, errors.Errorf("user %q doesn't exist", userRef))
		}

		if err := h.d.DeleteOrgMembersByUserID(tx, user.ID); err != nil {
			return util.NewAPIError(util.KindFromRemoteError(err), err)
		}

		if err := h.d.DeleteOrgInvitationsByUserID(tx, user.ID); err != nil {
			return util.NewAPIError(util.KindFromRemoteError(err), err)
		}

		if err := h.d.DeleteLinkedAccountsByUserID(tx, user.ID); err != nil {
			return util.NewAPIError(util.KindFromRemoteError(err), err)
		}

		if err := h.d.DeleteUserTokensByUserID(tx, user.ID); err != nil {
			return util.NewAPIError(util.KindFromRemoteError(err), err)
		}

		if err := h.d.DeleteUser(tx, user.ID); err != nil {
			return errors.WithStack(err)
		}

		return nil

	})
	if err != nil {
		return errors.WithStack(err)
	}

	return errors.WithStack(err)
}

type UpdateUserRequest struct {
	UserRef string

	UserName string
}

func (h *ActionHandler) UpdateUser(ctx context.Context, req *UpdateUserRequest) (*types.User, error) {
	var user *types.User

	err := h.d.Do(ctx, func(tx *sql.Tx) error {
		var err error
		user, err = h.d.GetUser(tx, req.UserRef)
		if err != nil {
			return errors.WithStack(err)
		}
		if user == nil {
			return util.NewAPIError(util.ErrBadRequest, errors.Errorf("user %q doesn't exist", req.UserRef))
		}

		if req.UserName != "" {
			// check duplicate user name
			u, err := h.d.GetUserByName(tx, req.UserName)
			if err != nil {
				return errors.WithStack(err)
			}
			if u != nil {
				return util.NewAPIError(util.ErrBadRequest, errors.Errorf("user with name %q already exists", u.Name))
			}

			user.Name = req.UserName
		}

		if err := h.d.UpdateUser(tx, user); err != nil {
			return errors.WithStack(err)
		}

		return nil
	})
	if err != nil {
		return nil, errors.WithStack(err)
	}

	return user, errors.WithStack(err)
}

func (h *ActionHandler) GetUserLinkedAccounts(ctx context.Context, userRef string) ([]*types.LinkedAccount, error) {
	if userRef == "" {
		return nil, util.NewAPIError(util.ErrBadRequest, errors.Errorf("user ref required"))
	}

	var linkedAccounts []*types.LinkedAccount
	err := h.d.Do(ctx, func(tx *sql.Tx) error {
		user, err := h.d.GetUser(tx, userRef)
		if err != nil {
			return errors.WithStack(err)
		}
		if user == nil {
			return util.NewAPIError(util.ErrBadRequest, errors.Errorf("user %q doesn't exist", userRef))
		}

		linkedAccounts, err = h.d.GetUserLinkedAccounts(tx, user.ID)
		if err != nil {
			return errors.WithStack(err)
		}

		return nil
	})
	if err != nil {
		return nil, errors.WithStack(err)
	}

	return linkedAccounts, errors.WithStack(err)
}

type CreateUserLARequest struct {
	UserRef string

	RemoteSourceName           string
	RemoteUserID               string
	RemoteUserName             string
	UserAccessToken            string
	Oauth2AccessToken          string
	Oauth2RefreshToken         string
	Oauth2AccessTokenExpiresAt time.Time
}

func (h *ActionHandler) CreateUserLA(ctx context.Context, req *CreateUserLARequest) (*types.LinkedAccount, error) {
	if req.UserRef == "" {
		return nil, util.NewAPIError(util.ErrBadRequest, errors.Errorf("user ref required"))
	}
	if req.RemoteSourceName == "" {
		return nil, util.NewAPIError(util.ErrBadRequest, errors.Errorf("remote source name required"))
	}

	var la *types.LinkedAccount
	err := h.d.Do(ctx, func(tx *sql.Tx) error {
		user, err := h.d.GetUser(tx, req.UserRef)
		if err != nil {
			return errors.WithStack(err)
		}
		if user == nil {
			return util.NewAPIError(util.ErrBadRequest, errors.Errorf("user %q doesn't exist", req.UserRef))
		}

		rs, err := h.d.GetRemoteSourceByName(tx, req.RemoteSourceName)
		if err != nil {
			return errors.WithStack(err)
		}
		if rs == nil {
			return util.NewAPIError(util.ErrBadRequest, errors.Errorf("remote source %q doesn't exist", req.RemoteSourceName))
		}

		la, err = h.d.GetLinkedAccountByRemoteUserIDandSource(tx, req.RemoteUserID, rs.ID)
		if err != nil {
			return errors.Wrapf(err, "failed to get linked account for remote user id %q and remote source %q", req.RemoteUserID, rs.ID)
		}
		if la != nil {
			return util.NewAPIError(util.ErrBadRequest, errors.Errorf("linked account for remote user id %q for remote source %q already exists", req.RemoteUserID, req.RemoteSourceName))
		}

		la = types.NewLinkedAccount(tx)
		la.UserID = user.ID
		la.RemoteSourceID = rs.ID
		la.RemoteUserID = req.RemoteUserID
		la.RemoteUserName = req.RemoteUserName
		la.UserAccessToken = req.UserAccessToken
		la.Oauth2AccessToken = req.Oauth2AccessToken
		la.Oauth2RefreshToken = req.Oauth2RefreshToken
		la.Oauth2AccessTokenExpiresAt = req.Oauth2AccessTokenExpiresAt

		if err := h.d.InsertLinkedAccount(tx, la); err != nil {
			return errors.WithStack(err)
		}

		return nil
	})
	if err != nil {
		return nil, errors.WithStack(err)
	}

	return la, errors.WithStack(err)
}

func (h *ActionHandler) DeleteUserLA(ctx context.Context, userRef, laID string) error {
	if userRef == "" {
		return util.NewAPIError(util.ErrBadRequest, errors.Errorf("user ref  required"))
	}
	if laID == "" {
		return util.NewAPIError(util.ErrBadRequest, errors.Errorf("user linked account id required"))
	}

	var user *types.User

	err := h.d.Do(ctx, func(tx *sql.Tx) error {
		var err error
		user, err = h.d.GetUser(tx, userRef)
		if err != nil {
			return errors.WithStack(err)
		}
		if user == nil {
			return util.NewAPIError(util.ErrBadRequest, errors.Errorf("user %q doesn't exist", userRef))
		}

		la, err := h.d.GetLinkedAccount(tx, laID)
		if err != nil {
			return errors.WithStack(err)
		}
		if la == nil {
			return util.NewAPIError(util.ErrBadRequest, errors.Errorf("linked account id %q for user %q doesn't exist", laID, userRef))
		}

		// check that the linked account belongs to the right user
		if user.ID != la.UserID {
			return util.NewAPIError(util.ErrBadRequest, errors.Errorf("linked account id %q for user %q doesn't exist", laID, userRef))
		}

		if err := h.d.DeleteLinkedAccount(tx, la.ID); err != nil {
			return errors.WithStack(err)
		}

		return nil
	})
	if err != nil {
		return errors.WithStack(err)
	}

	return errors.WithStack(err)
}

type UpdateUserLARequest struct {
	UserRef string

	LinkedAccountID            string
	RemoteUserID               string
	RemoteUserName             string
	UserAccessToken            string
	Oauth2AccessToken          string
	Oauth2RefreshToken         string
	Oauth2AccessTokenExpiresAt time.Time
}

func (h *ActionHandler) UpdateUserLA(ctx context.Context, req *UpdateUserLARequest) (*types.LinkedAccount, error) {
	if req.UserRef == "" {
		return nil, util.NewAPIError(util.ErrBadRequest, errors.Errorf("user ref required"))
	}

	var la *types.LinkedAccount
	err := h.d.Do(ctx, func(tx *sql.Tx) error {
		user, err := h.d.GetUser(tx, req.UserRef)
		if err != nil {
			return errors.WithStack(err)
		}
		if user == nil {
			return util.NewAPIError(util.ErrBadRequest, errors.Errorf("user %q doesn't exist", req.UserRef))
		}

		la, err = h.d.GetLinkedAccount(tx, req.LinkedAccountID)
		if err != nil {
			return errors.WithStack(err)
		}
		if la == nil {
			return util.NewAPIError(util.ErrBadRequest, errors.Errorf("linked account id %q for user %q doesn't exist", req.LinkedAccountID, req.UserRef))
		}

		// check that the linked account belongs to the right user
		if user.ID != la.UserID {
			return util.NewAPIError(util.ErrBadRequest, errors.Errorf("linked account id %q for user %q doesn't exist", req.LinkedAccountID, req.UserRef))
		}

		rs, err := h.d.GetRemoteSource(tx, la.RemoteSourceID)
		if err != nil {
			return errors.WithStack(err)
		}
		if rs == nil {
			return util.NewAPIError(util.ErrBadRequest, errors.Errorf("remote source with id %q doesn't exist", la.RemoteSourceID))
		}

		la.RemoteUserID = req.RemoteUserID
		la.RemoteUserName = req.RemoteUserName
		la.UserAccessToken = req.UserAccessToken
		la.Oauth2AccessToken = req.Oauth2AccessToken
		la.Oauth2RefreshToken = req.Oauth2RefreshToken
		la.Oauth2AccessTokenExpiresAt = req.Oauth2AccessTokenExpiresAt

		if err := h.d.UpdateLinkedAccount(tx, la); err != nil {
			return errors.WithStack(err)
		}

		return nil
	})
	if err != nil {
		return nil, errors.WithStack(err)
	}

	return la, errors.WithStack(err)
}

func (h *ActionHandler) GetUserTokens(ctx context.Context, userRef string) ([]*types.UserToken, error) {
	if userRef == "" {
		return nil, util.NewAPIError(util.ErrBadRequest, errors.Errorf("user ref required"))
	}

	var tokens []*types.UserToken
	err := h.d.Do(ctx, func(tx *sql.Tx) error {
		user, err := h.d.GetUser(tx, userRef)
		if err != nil {
			return errors.WithStack(err)
		}
		if user == nil {
			return util.NewAPIError(util.ErrBadRequest, errors.Errorf("user %q doesn't exist", userRef))
		}

		tokens, err = h.d.GetUserTokens(tx, user.ID)
		if err != nil {
			return errors.WithStack(err)
		}

		return nil
	})
	if err != nil {
		return nil, errors.WithStack(err)
	}

	return tokens, errors.WithStack(err)
}

func (h *ActionHandler) CreateUserToken(ctx context.Context, userRef, tokenName string) (*types.UserToken, error) {
	if userRef == "" {
		return nil, util.NewAPIError(util.ErrBadRequest, errors.Errorf("user ref required"))
	}
	if tokenName == "" {
		return nil, util.NewAPIError(util.ErrBadRequest, errors.Errorf("token name required"))
	}

	var token *types.UserToken
	err := h.d.Do(ctx, func(tx *sql.Tx) error {
		user, err := h.d.GetUser(tx, userRef)
		if err != nil {
			return errors.WithStack(err)
		}
		if user == nil {
			return util.NewAPIError(util.ErrBadRequest, errors.Errorf("user %q doesn't exist", userRef))
		}

		userToken, err := h.d.GetUserToken(tx, user.ID, tokenName)
		if err != nil {
			return errors.WithStack(err)
		}

		if userToken != nil {
			return util.NewAPIError(util.ErrBadRequest, errors.Errorf("token %q for user %q already exists", tokenName, userRef))
		}

		token = types.NewUserToken(tx)
		token.UserID = user.ID
		token.Name = tokenName
		token.Value = util.EncodeSha1Hex(uuid.Must(uuid.NewV4()).String())

		if err := h.d.InsertUserToken(tx, token); err != nil {
			return errors.WithStack(err)
		}

		return nil
	})
	if err != nil {
		return nil, errors.WithStack(err)
	}

	return token, errors.WithStack(err)
}

func (h *ActionHandler) DeleteUserToken(ctx context.Context, userRef, tokenName string) error {
	if userRef == "" {
		return util.NewAPIError(util.ErrBadRequest, errors.Errorf("user ref required"))
	}
	if tokenName == "" {
		return util.NewAPIError(util.ErrBadRequest, errors.Errorf("token name required"))
	}

	err := h.d.Do(ctx, func(tx *sql.Tx) error {
		user, err := h.d.GetUser(tx, userRef)
		if err != nil {
			return errors.WithStack(err)
		}
		if user == nil {
			return util.NewAPIError(util.ErrBadRequest, errors.Errorf("user %q doesn't exist", userRef))
		}

		userToken, err := h.d.GetUserToken(tx, user.ID, tokenName)
		if err != nil {
			return errors.WithStack(err)
		}

		if userToken == nil {
			return util.NewAPIError(util.ErrBadRequest, errors.Errorf("token %q for user %q doesn't exist", tokenName, userRef))
		}

		if err := h.d.DeleteUserToken(tx, userToken.ID); err != nil {
			return errors.WithStack(err)
		}

		return nil
	})
	if err != nil {
		return errors.WithStack(err)
	}

	return errors.WithStack(err)
}

type UserOrg struct {
	Organization *types.Organization
	Role         types.MemberRole
}

func userOrgResponse(userOrg *db.UserOrg) *UserOrg {
	return &UserOrg{
		Organization: userOrg.Organization,
		Role:         userOrg.Role,
	}
}

func (h *ActionHandler) GetUserOrg(ctx context.Context, userRef, orgRef string) (*UserOrg, error) {
	var dbUserOrg *db.UserOrg

	err := h.d.Do(ctx, func(tx *sql.Tx) error {
		var err error
		user, err := h.d.GetUser(tx, userRef)
		if err != nil {
			return errors.WithStack(err)
		}
		if user == nil {
			return util.NewAPIError(util.ErrNotExist, errors.Errorf("user %q doesn't exist", userRef))
		}
		org, err := h.d.GetOrg(tx, orgRef)
		if err != nil {
			return errors.WithStack(err)
		}
		if org == nil {
			return util.NewAPIError(util.ErrNotExist, errors.Errorf("org %q doesn't exist", orgRef))
		}

		dbUserOrg, err = h.d.GetUserOrg(tx, user.ID, org.ID)
		return errors.WithStack(err)
	})
	if err != nil {
		return nil, errors.WithStack(err)
	}

	if dbUserOrg == nil {
		return nil, util.NewAPIError(util.ErrNotExist, errors.Errorf("user %q is not member of org %q", userRef, orgRef))
	}

	userOrg := userOrgResponse(dbUserOrg)

	return userOrg, nil
}

type GetUserOrgsRequest struct {
	UserRef      string
	StartOrgName string

	Limit         int
	SortDirection types.SortDirection
}

type GetUserOrgsResponse struct {
	UserOrgs []*UserOrg

	HasMore bool
}

func (h *ActionHandler) GetUserOrgs(ctx context.Context, req *GetUserOrgsRequest) (*GetUserOrgsResponse, error) {
	limit := req.Limit
	if limit > 0 {
		limit += 1
	}

	var dbUserOrgs []*db.UserOrg
	err := h.d.Do(ctx, func(tx *sql.Tx) error {
		var err error
		user, err := h.d.GetUser(tx, req.UserRef)
		if err != nil {
			return errors.WithStack(err)
		}
		if user == nil {
			return util.NewAPIError(util.ErrNotExist, errors.Errorf("user %q doesn't exist", req.UserRef))
		}

		dbUserOrgs, err = h.d.GetUserOrgs(tx, user.ID, req.StartOrgName, limit, req.SortDirection)
		return errors.WithStack(err)
	})
	if err != nil {
		return nil, errors.WithStack(err)
	}

	userOrgs := make([]*UserOrg, len(dbUserOrgs))
	for i, dbUserOrg := range dbUserOrgs {
		userOrgs[i] = userOrgResponse(dbUserOrg)
	}

	var hasMore bool
	if req.Limit > 0 {
		hasMore = len(userOrgs) > req.Limit
		if hasMore {
			userOrgs = userOrgs[0:req.Limit]
		}
	}

	return &GetUserOrgsResponse{
		UserOrgs: userOrgs,
		HasMore:  hasMore,
	}, nil
}

func (h *ActionHandler) GetUserOrgInvitations(ctx context.Context, userRef string) ([]*types.OrgInvitation, error) {
	var orgInvitations []*types.OrgInvitation
	err := h.d.Do(ctx, func(tx *sql.Tx) error {
		user, err := h.d.GetUser(tx, userRef)
		if err != nil {
			return errors.WithStack(err)
		}
		if user == nil {
			return util.NewAPIError(util.ErrBadRequest, errors.Errorf("user %q doesn't exist", userRef))
		}

		orgInvitations, err = h.d.GetOrgInvitationByUserID(tx, user.ID)
		if err != nil {
			return errors.WithStack(err)
		}

		return errors.WithStack(err)
	})
	if err != nil {
		return nil, errors.WithStack(err)
	}

	return orgInvitations, errors.WithStack(err)
}
