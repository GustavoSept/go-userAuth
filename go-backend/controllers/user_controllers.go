package controllers

import (
	"encoding/hex"
	"fmt"
	"log"
	"net/http"

	"go-backend/database"
	"go-backend/helpers"
	"go-backend/models"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const (
	SALT_SIZE = 16
)

func GetAllUsers(c *gin.Context) {
	var usersList []models.User
	err := database.DB.Select(&usersList, "SELECT * FROM users")

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, usersList)
}

func GetAllUsersAuth(c *gin.Context) {
	var usersAuthList []models.User_Auth
	err := database.DB.Select(&usersAuthList, "SELECT * FROM users_authentication")

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, usersAuthList)
}

func CreateUser(c *gin.Context) {
	var newUser models.User
	var newUserAuth models.User_Auth
	var password models.Password

	var salt []byte
	var hashedPass string
	var err error

	// Attempt to parse request body into models
	if err := c.Bind(&newUser); err != nil {
		return
	}
	if err := c.Bind(&newUserAuth); err != nil {
		return
	}

	password.RawPassword = c.PostForm("raw_password")
	const lenPass = 8

	// Validate newUser
	if err := models.Validate.Struct(newUser); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := models.Validate.Struct(newUserAuth); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := models.Validate.Struct(password); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// ------------------------ Check parkingLotID
	// If user is not an owner, and there's no parkingLotID, we can't create the user
	// Owner users can be created without being immediately associated with a parkingLotID
	plID, pl_exists := c.Get("parkingLotID")
	if !pl_exists && newUser.Office_Level != "dono" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Can't create non-owner user without a parking lot"})
		return
	}

	// Start new transaction
	tx, err := database.DB.Beginx()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Generate a UUID
	newUser.ID = uuid.New()
	newUserAuth.UserID = newUser.ID

	// Generate Salt
	salt, err = helpers.GenerateSalt(SALT_SIZE)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	log.Println("<<<<<<<<<<<<<<<<<<<< GENERATING NEW USER >>>>>>>>>>>>>>>>>>>>>>>>>>")
	log.Printf("Generating salt (of type: %T) for user. Salt: %v", salt, salt)

	// Hash Password
	hashedPass, err = helpers.HashPassword(password.RawPassword, salt)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Insert newUser into users table
	_, err = tx.NamedExec(
		`INSERT INTO users (id, first_name, last_name, office_level)
						VALUES (:id, :first_name, :last_name, :office_level)`, &newUser)
	if err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Convert salt to hexadecimal
	hexSalt := make([]byte, hex.EncodedLen(len(salt)))
	hex.Encode(hexSalt, salt)

	log.Printf("User's Raw Password (of type: %T): %v", password.RawPassword, password.RawPassword)
	log.Printf("Generating user's hashedPass (of type: %T): %v", hashedPass, hashedPass)
	log.Printf("Storing user's hexSalt (of type: %T): %v", hexSalt, hexSalt)

	newUserAuth.Salt = hexSalt
	newUserAuth.PasswordHash = hashedPass

	// Insert newUserAuth into users_authentication table
	_, err = tx.NamedExec(`
						INSERT INTO users_authentication (user_id, email, password_hash, salt)
						VALUES (:user_id, :email, :password_hash, :salt)`, &newUserAuth)
	if err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Insert into parking_lot_employees if necessary
	if pl_exists && newUser.Office_Level != "dono" {
		_, err = tx.Exec(`
						INSERT INTO parking_lot_employees (user_id, parking_lot_id)
						VALUES ($1, $2)`, newUser.ID, plID)
		if err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}

	// JWT token creation
	authTokenString, refreshTokenString, csrfSecret, err := helpers.CreateNewTokens(newUser.ID.String(), newUser.Office_Level)
	if err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create tokens"})
		return
	}

	// Commit the transaction
	if err = tx.Commit(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":      fmt.Sprintf("Bazinga! A conta de %s foi criada com sucesso!", newUser.First_Name),
		"authToken":    authTokenString,
		"refreshToken": refreshTokenString,
		"csrfSecret":   csrfSecret,
	})
}

func LoginUser(c *gin.Context) {
	var loginForm struct {
		InEmail    string `form:"input_email"`
		InPassword string `form:"input_password"`
	}
	if err := c.ShouldBind(&loginForm); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	user_uuid, user_officeLevel, err := helpers.MatchCredentialsWithUser(loginForm.InPassword, loginForm.InEmail)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// Generate and set cookies
	authTokenString, refreshTokenString, csrfSecret, err := helpers.CreateNewTokens(user_uuid.String(), user_officeLevel)
	log.Printf(`
		Logged user in!
		Generating the following cookies:
		authToken: %v
		refreshToken: %v
		csrfSecret: %v
	`, authTokenString, refreshTokenString, csrfSecret)
	if err != nil {
		log.Println("error: Failed to create tokens")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create tokens"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"authToken":    authTokenString,
		"refreshToken": refreshTokenString,
		"csrfSecret":   csrfSecret,
	})
}

func LogUserOut(c *gin.Context) {
	helpers.NullifyTokenCookies(c)
	c.Redirect(http.StatusFound, "/login")
}

func DeleteUser(c *gin.Context) {
	// Get and validate AuthToken
	authToken, err := c.Cookie("AuthToken")
	if err != nil {
		helpers.NullifyTokenCookies(c)
		c.Redirect(http.StatusFound, "/login")
		return
	}

	uuid, err := helpers.GrabUUID(authToken)
	if err != nil {
		helpers.NullifyTokenCookies(c)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to process token"})
		return
	}

	// Start transaction
	tx, err := database.DB.Beginx()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal Server Error"})
		return
	}

	/* Should CASCADE to the following tables as well:
	- users_authentication
	- parking_lot_employees
	- parking_lots
	*/
	_, err = tx.NamedExec("DELETE FROM users WHERE id = $1", uuid)
	if err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal Server Error"})
		return
	}

	err = tx.Commit()
	if err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal Server Error"})
		return
	}

	helpers.NullifyTokenCookies(c)
	c.Redirect(http.StatusFound, "/register")
}
