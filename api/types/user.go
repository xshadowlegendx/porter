package types

type User struct {
	ID            uint   `json:"id"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
}

type CreateUserRequest struct {
	Email    string `json:"email" form:"required,max=255,email"`
	Password string `json:"password" form:"required,max=255"`
}

type CreateUserResponse User

type GetAuthenticatedUserResponse User

type LoginUserRequest struct {
	Email    string `json:"email" form:"required,max=255,email"`
	Password string `json:"password" form:"required,max=255"`
}

type LoginUserResponse User

type CLILoginUserRequest struct {
	Redirect string `schema:"redirect" form:"required"`
}

type CLILoginExchangeRequest struct {
	AuthorizationCode string `json:"authorization_code" form:"required"`
}

type CLILoginExchangeResponse struct {
	Token string `json:"token" form:"required"`
}

type InitiateResetUserPasswordRequest struct {
	Email string `json:"email" form:"required"`
}

type VerifyTokenFinalizeRequest struct {
	TokenID uint   `schema:"token_id" form:"required"`
	Token   string `schema:"token" form:"required"`
}

type VerifyEmailFinalizeRequest struct {
	VerifyTokenFinalizeRequest
}

type VerifyResetUserPasswordRequest struct {
	VerifyTokenFinalizeRequest

	Email string `json:"email" form:"required,max=255,email"`
}

type FinalizeResetUserPasswordRequest struct {
	VerifyResetUserPasswordRequest

	NewPassword string `json:"new_password" form:"required,max=255"`
}

type ListUserProjectsResponse []*Project
