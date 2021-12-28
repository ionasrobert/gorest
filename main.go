package main

import (
	// "fmt"

	"fmt"
	"io"
	"os"
	"time"

	"github.com/pilinux/gorest/config"
	"github.com/pilinux/gorest/controller"
	"github.com/pilinux/gorest/database"
	"github.com/pilinux/gorest/lib/middleware"

	"github.com/gin-gonic/gin"
)

var configure = config.Config()

func main() {
	if err := database.InitDB().Error; err != nil {
		fmt.Println(err)
	}

	if configure.Database.REDIS.Activate == "yes" {
		// Initialize REDIS client
		database.InitRedis()
	}

	// JWT
	middleware.AccessKey = []byte(configure.Security.JWT.AccessKey)
	middleware.AccessKeyTTL = configure.Security.JWT.AccessKeyTTL
	middleware.RefreshKey = []byte(configure.Security.JWT.RefreshKey)
	middleware.RefreshKeyTTL = configure.Security.JWT.RefreshKeyTTL

	// Debugging - environment variables
	/*
		fmt.Println(configure.Server.ServerPort)
		fmt.Println(configure.Database.DbDriver)
		fmt.Println(configure.Database.DbUser)
		fmt.Println(configure.Database.DbPass)
		fmt.Println(configure.Database.DbName)
		fmt.Println(configure.Database.DbHost)
		fmt.Println(configure.Database.DbPort)
	*/

	router := SetupRouter()
	router.Run(":" + configure.Server.ServerPort)
}

// SetupRouter ...
func SetupRouter() *gin.Engine {
	if configure.Server.ServerEnv == "production" {
		gin.SetMode(gin.ReleaseMode) // Omit this line to enable debug mode
	}

	// Write log file
	// Console color is not required to write the logs to the file
	//	gin.DisableConsoleColor()

	// Create a log file with start time
	dt := time.Now()
	t := dt.Format(time.RFC3339)
	file, _ := os.Create("./logs/start:" + t + ".log")
	// gin.DefaultWriter = io.MultiWriter(file)

	// If it is required to write the logs to the file and the console
	// at the same time
	gin.DefaultWriter = io.MultiWriter(file, os.Stdout)

	// Creates a router without any middleware by default
	// router := gin.New()

	// Logger middleware: gin.DefaultWriter = os.Stdout
	// router.Use(gin.Logger())

	// Recovery middleware recovers from any panics and writes a 500
	// if there is one
	// router.Use(gin.Recovery())

	// gin.Default() = gin.New() + gin.Logger() + gin.Recovery()
	router := gin.Default()

	// Which proxy to trust
	if configure.Security.TrustedIP == "nil" {
		router.SetTrustedProxies(nil)
	} else {
		if configure.Security.TrustedIP != "" {
			router.SetTrustedProxies([]string{configure.Security.TrustedIP})
		}
	}

	router.Use(middleware.CORS())
	router.Use(middleware.SentryCapture(configure.Logger.SentryDsn))

	// API:v1.0
	v1 := router.Group("/api/v1/")
	{
		// Register - no JWT required
		v1.POST("register", controller.CreateUserAuth)

		// Login - app issues JWT
		v1.POST("login", controller.Login)

		// Refresh - app issues new JWT
		rJWT := v1.Group("refresh")
		rJWT.Use(middleware.RefreshJWT())
		rJWT.POST("", controller.Refresh)

		// User
		rUsers := v1.Group("users")
		rUsers.GET("", controller.GetUsers)    // Non-protected
		rUsers.GET("/:id", controller.GetUser) // Non-protected
		rUsers.Use(middleware.JWT())
		rUsers.POST("", controller.CreateUser)      // Protected
		rUsers.PUT("", controller.UpdateUser)       // Protected
		rUsers.PUT("/hobbies", controller.AddHobby) // Protected

		// Post
		rPosts := v1.Group("posts")
		rPosts.GET("", controller.GetPosts)    // Non-protected
		rPosts.GET("/:id", controller.GetPost) // Non-protected
		rPosts.Use(middleware.JWT())
		rPosts.POST("", controller.CreatePost)       // Protected
		rPosts.PUT("/:id", controller.UpdatePost)    // Protected
		rPosts.DELETE("/:id", controller.DeletePost) // Protected

		// Hobby
		rHobbies := v1.Group("hobbies")
		rHobbies.GET("", controller.GetHobbies) // Non-protected

		// Playground
		if configure.Database.REDIS.Activate == "yes" {
			rPlayground := v1.Group("playground")
			rPlayground.GET("/redis_read", controller.RedisRead)        // Non-protected
			rPlayground.POST("/redis_create", controller.RedisCreate)   // Non-protected
			rPlayground.DELETE("/redis_delete", controller.RedisDelete) // Non-protected

			rPlayground.GET("/redis_read_hash", controller.RedisReadHash)        // Non-protected
			rPlayground.POST("/redis_create_hash", controller.RedisCreateHash)   // Non-protected
			rPlayground.DELETE("/redis_delete_hash", controller.RedisDeleteHash) // Non-protected
		}
	}

	return router
}
