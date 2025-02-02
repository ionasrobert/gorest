package service

import (
	"context"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/mediocregopher/radix/v4"
	log "github.com/sirupsen/logrus"

	"github.com/pilinux/gorest/config"
	"github.com/pilinux/gorest/database"
	"github.com/pilinux/gorest/database/model"
	"github.com/pilinux/gorest/lib"
	"github.com/pilinux/gorest/lib/middleware"
	"github.com/pilinux/libgo/timestring"
)

// GetClaims - get JWT custom claims
func GetClaims(c *gin.Context) middleware.MyCustomClaims {
	// get claims
	claims := middleware.MyCustomClaims{
		AuthID:  c.GetUint64("authID"),
		Email:   c.GetString("email"),
		Role:    c.GetString("role"),
		Scope:   c.GetString("scope"),
		TwoFA:   c.GetString("tfa"),
		SiteLan: c.GetString("siteLan"),
		Custom1: c.GetString("custom1"),
		Custom2: c.GetString("custom2"),
	}

	return claims
}

// ValidateAuthID - check whether authID is missing
func ValidateAuthID(authID uint64) bool {
	return authID != 0
}

// ValidateUserID - check whether authID or email is missing
func ValidateUserID(authID uint64, email string) bool {
	email = strings.TrimSpace(email)
	return authID != 0 && email != ""
}

// Validate2FA validates user-provided OTP
func Validate2FA(encryptedMessage []byte, issuer string, userInput string) ([]byte, string, error) {
	configSecurity := config.GetConfig().Security
	otpByte, err := lib.ValidateTOTP(encryptedMessage, issuer, userInput)
	// client provided invalid OTP / internal error
	if err != nil {
		// client provided invalid OTP
		if len(otpByte) > 0 {
			return otpByte, configSecurity.TwoFA.Status.Invalid, err
		}

		// internal error
		return []byte{}, "", err
	}

	// validated
	return otpByte, configSecurity.TwoFA.Status.Verified, nil
}

// DelMem2FA - delete secrets from memory
func DelMem2FA(authID uint64) {
	delete(model.InMemorySecret2FA, authID)
}

// SendEmail sends a verification/password recovery email if
//
// - required by the application
//
// - an external email service is configured
//
// - a redis database is configured
//
// {true, nil} => email delivered successfully
//
// {false, nil} => email delivery not required/service not configured
//
// {false, error} => email delivery failed
func SendEmail(email string, emailType int) (bool, error) {
	// send email if required by the application
	appConfig := config.GetConfig()

	// is external email service activated
	if appConfig.EmailConf.Activate != config.Activated {
		return false, nil
	}

	// is verification/password recovery email required
	doSendEmail := false
	if appConfig.Security.VerifyEmail && emailType == model.EmailTypeVerifyEmailNewAcc {
		doSendEmail = true
	}
	if appConfig.Security.RecoverPass && emailType == model.EmailTypePassRecovery {
		doSendEmail = true
	}
	if appConfig.Security.VerifyEmail && emailType == model.EmailTypeVerifyUpdatedEmail {
		doSendEmail = true
	}
	if !doSendEmail {
		return false, nil
	}

	// is redis database activated
	if appConfig.Database.REDIS.Activate != config.Activated {
		return false, nil
	}

	data := struct {
		key   string
		value string
	}{}
	var keyTTL uint64
	var emailTag string
	var code uint64

	// generate verification/password recovery code
	if emailType == model.EmailTypeVerifyEmailNewAcc || emailType == model.EmailTypeVerifyUpdatedEmail {
		code = lib.SecureRandomNumber(appConfig.EmailConf.EmailVerificationCodeLength)
		data.key = model.EmailVerificationKeyPrefix + strconv.FormatUint(code, 10)
		keyTTL = appConfig.EmailConf.EmailVerifyValidityPeriod
		emailTag = appConfig.EmailConf.EmailVerificationTag
	}
	if emailType == model.EmailTypePassRecovery {
		code = lib.SecureRandomNumber(appConfig.EmailConf.PasswordRecoverCodeLength)
		data.key = model.PasswordRecoveryKeyPrefix + strconv.FormatUint(code, 10)
		keyTTL = appConfig.EmailConf.PassRecoverValidityPeriod
		emailTag = appConfig.EmailConf.PasswordRecoverTag
	}
	data.value = email

	// when encryption at rest is used
	if config.IsCipher() {
		var err error

		// hash of the email in hexadecimal string format
		value, err := CalcHash(
			[]byte(email),
			config.GetConfig().Security.Blake2bSec,
		)
		if err != nil {
			log.WithError(err).Error("error code: 406.1")
			return false, err
		}
		data.value = hex.EncodeToString(value)
	}

	// save in redis with expiry time
	client := *database.GetRedis()
	redisConnTTL := appConfig.Database.REDIS.Conn.ConnTTL

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(redisConnTTL)*time.Second)
	defer cancel()

	// Set key in Redis
	r1 := ""
	if err := client.Do(ctx, radix.FlatCmd(&r1, "SET", data.key, data.value)); err != nil {
		log.WithError(err).Error("error code: 401")
		return false, err
	}
	if r1 != "OK" {
		log.Error("error code: 402")
		return false, errors.New("failed to save in redis")
	}

	// Set expiry time
	r2 := 0
	if err := client.Do(ctx, radix.FlatCmd(&r2, "EXPIRE", data.key, keyTTL)); err != nil {
		log.WithError(err).Error("error code: 403")
	}
	if r2 != 1 {
		log.Error("error code: 404")
	}

	// check which email service
	// for Postmark
	if appConfig.EmailConf.Provider == "postmark" {
		htmlModel := lib.HTMLModel(lib.StrArrHTMLModel(appConfig.EmailConf.HTMLModel))
		htmlModel["secret_code"] = code
		htmlModel["email_validity_period"] = timestring.HourMinuteSecond(keyTTL)

		params := PostmarkParams{}
		params.ServerToken = appConfig.EmailConf.APIToken

		if emailType == model.EmailTypeVerifyEmailNewAcc {
			params.TemplateID = appConfig.EmailConf.EmailVerificationTemplateID
		}

		if emailType == model.EmailTypePassRecovery {
			params.TemplateID = appConfig.EmailConf.PasswordRecoverTemplateID
		}

		if emailType == model.EmailTypeVerifyUpdatedEmail {
			params.TemplateID = appConfig.EmailConf.EmailUpdateVerifyTemplateID
		}

		params.From = appConfig.EmailConf.AddrFrom
		params.To = email
		params.Tag = emailTag
		params.TrackOpens = appConfig.EmailConf.TrackOpens
		params.TrackLinks = appConfig.EmailConf.TrackLinks
		params.MessageStream = appConfig.EmailConf.DeliveryType
		params.HTMLModel = htmlModel

		// send the email
		res, err := Postmark(params)
		if err != nil {
			log.WithError(err).Error("error code: 405")
			return false, err
		}
		if res.Message != "OK" {
			return false, errors.New("email delivery failed")
		}

		return true, nil
	}

	e := errors.New(
		"email delivery service provider: '" + appConfig.EmailConf.Provider + "' is unknown",
	)
	log.WithError(e).Error("error code: 406")
	return false, e
}
