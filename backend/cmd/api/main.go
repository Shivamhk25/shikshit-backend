package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/pie360/backend/cmd/api/routes"
	"github.com/pie360/backend/internal/auth"
	"github.com/pie360/backend/internal/hub"
	"github.com/pie360/backend/internal/shared/config"
	"github.com/pie360/backend/internal/shared/db"
	"github.com/pie360/backend/internal/shared/logger"
	"github.com/pie360/backend/internal/tenant"
	"github.com/redis/go-redis/v9"
)

func main() {
	// Load configuration
	cfgPath := config.GetConfigPath()
	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		os.Exit(1)
	}

	// Initialize logger
	var log *logger.Logger
	if cfg.App.Env == "production" {
		log = logger.NewProduction()
	} else {
		log = logger.NewDefault()
	}
	log.Info().
		Str("version", cfg.App.Version).
		Str("env", cfg.App.Env).
		Msg("Starting PIE360 Backend")

	// Initialize database
	dbPool, err := db.NewPool(context.Background(), &cfg.Database, log)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to connect to database")
	}
	defer dbPool.Close()

	// Initialize Redis
	redisClient := redis.NewClient(&redis.Options{
		Addr:         fmt.Sprintf("%s:%d", cfg.Redis.Host, cfg.Redis.Port),
		Password:     cfg.Redis.Password,
		DB:           cfg.Redis.DB,
		PoolSize:     cfg.Redis.PoolSize,
		MinIdleConns: cfg.Redis.MinIdleConns,
		DialTimeout:  cfg.Redis.DialTimeout,
		ReadTimeout:  cfg.Redis.ReadTimeout,
		WriteTimeout: cfg.Redis.WriteTimeout,
	})
	defer redisClient.Close()

	// Test Redis connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := redisClient.Ping(ctx).Err(); err != nil {
		log.Fatal().Err(err).Msg("Failed to connect to Redis")
	}

	// RedisClient wrapper for auth.RedisClient interface
	redisWrapper := &redisWrapper{client: redisClient}

	// Initialize auth service
	authSvc, err := auth.NewAuthService(cfg, log)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to initialize auth service")
	}

	// Initialize session manager
	sessionMgr := auth.NewSessionManager(dbPool, cfg, log, redisWrapper)

	// Initialize Casbin enforcer
	if _, err := auth.NewEnforcer(cfg, log, redisWrapper); err != nil {
		log.Fatal().Err(err).Msg("Failed to initialize Casbin enforcer")
	}

	// Initialize WebSocket hub
	wsHub := hub.NewHub(&cfg.WebSocket, log)
	wsHub.Start()
	defer wsHub.Stop()

	// Initialize tenant middleware
	tenantMW := tenant.NewTenantMiddleware(dbPool, authSvc, sessionMgr, cfg, log)

	// Initialize Echo
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true

	// Global middleware
	e.Use(middleware.Recover())
	e.Use(middleware.RequestLoggerWithConfig(middleware.RequestLoggerConfig{
		LogURI:      true,
		LogMethod:   true,
		LogStatus:   true,
		LogLatency:  true,
		LogError:    true,
		HandleError: true,
		LogValuesFunc: func(c echo.Context, v middleware.RequestLoggerValues) error {
			log.Info().
				Str("method", v.Method).
				Str("uri", v.URI).
				Int("status", v.Status).
				Dur("latency", v.Latency).
				Str("ip", c.RealIP()).
				Msg("HTTP request")
			return nil
		},
	}))

	// Security headers
	e.Use(tenant.SecurityHeadersMiddleware())
	
	// CORS
	e.Use(tenant.CORSMiddleware(&cfg.CORS))
	
	// Request ID
	e.Use(tenant.RequestIDMiddleware())

	// Health check endpoint (no auth required)
	e.GET("/health", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]interface{}{
			"status":    "healthy",
			"version":   cfg.App.Version,
			"timestamp": time.Now().Unix(),
			"services": map[string]string{
				"database": "connected",
				"redis":    "connected",
			},
		})
	})

	// Metrics endpoint
	if cfg.Metrics.Enabled {
		e.GET(cfg.Metrics.Path, func(c echo.Context) error {
			stats := wsHub.GetStats()
			return c.JSON(http.StatusOK, map[string]interface{}{
				"websocket": stats,
				"uptime":    time.Since(startTime).String(),
			})
		})
	}

	// API routes
	api := e.Group("/api/v1")
	
	// Public routes (no auth)
	public := api.Group("")
	public.POST("/auth/login", routes.LoginHandler(authSvc, sessionMgr, log))
	public.POST("/auth/refresh", routes.RefreshHandler(authSvc, sessionMgr, log))
	public.POST("/auth/forgot-password", routes.ForgotPasswordHandler(authSvc, log))
	public.POST("/auth/reset-password", routes.ResetPasswordHandler(authSvc, log))
	public.POST("/auth/verify-email", routes.VerifyEmailHandler(authSvc, log))
	public.GET("/auth/captcha", routes.GetCaptchaHandler(authSvc, log))

	// Protected routes (require auth)
	protected := api.Group("")
	protected.Use(tenantMW.RequireAuth())
	protected.Use(tenantMW.RequireTenant())

	// Auth routes
	protected.POST("/auth/logout", routes.LogoutHandler(sessionMgr, log))
	protected.GET("/auth/me", routes.MeHandler(log))
	protected.POST("/auth/change-password", routes.ChangePasswordHandler(authSvc, log))
	protected.POST("/auth/mfa/enable", routes.EnableMFAHandler(authSvc, log))
	protected.POST("/auth/mfa/disable", routes.DisableMFAHandler(authSvc, log))
	protected.GET("/auth/sessions", routes.ListSessionsHandler(sessionMgr, log))
	protected.DELETE("/auth/sessions/:id", routes.RevokeSessionHandler(sessionMgr, log))
	protected.DELETE("/auth/sessions", routes.RevokeAllSessionsHandler(sessionMgr, log))

	// User routes
	users := protected.Group("/users")
	users.GET("", routes.ListUsersHandler(log))
	users.GET("/:id", routes.GetUserHandler(log))
	users.PUT("/:id", routes.UpdateUserHandler(log))
	users.DELETE("/:id", routes.DeleteUserHandler(log))
	users.GET("/:id/roles", routes.GetUserRolesHandler(log))
	users.POST("/:id/roles", routes.AssignUserRoleHandler(log))
	users.DELETE("/:id/roles/:role", routes.RemoveUserRoleHandler(log))

	// Institute routes (admin only)
	institutes := protected.Group("/institutes")
	institutes.Use(tenantMW.RequireRole("super_admin", "institute_admin"))
	institutes.GET("", routes.ListInstitutesHandler(log))
	institutes.POST("", routes.CreateInstituteHandler(log))
	institutes.GET("/:id", routes.GetInstituteHandler(log))
	institutes.PUT("/:id", routes.UpdateInstituteHandler(log))
	institutes.DELETE("/:id", routes.DeleteInstituteHandler(log))

	// Academic routes
	academic := protected.Group("/academic")
	academic.GET("/years", routes.ListAcademicYearsHandler(log))
	academic.POST("/years", routes.CreateAcademicYearHandler(log))
	academic.GET("/terms", routes.ListTermsHandler(log))
	academic.POST("/terms", routes.CreateTermHandler(log))
	academic.GET("/departments", routes.ListDepartmentsHandler(log))
	academic.POST("/departments", routes.CreateDepartmentHandler(log))
	academic.GET("/subjects", routes.ListSubjectsHandler(log))
	academic.POST("/subjects", routes.CreateSubjectHandler(log))
	academic.GET("/classes", routes.ListClassesHandler(log))
	academic.POST("/classes", routes.CreateClassHandler(log))
	academic.GET("/classes/:id", routes.GetClassHandler(log))
	academic.PUT("/classes/:id", routes.UpdateClassHandler(log))
	academic.POST("/classes/:id/enroll", routes.EnrollStudentHandler(log))
	academic.DELETE("/classes/:id/enroll/:studentId", routes.UnenrollStudentHandler(log))
	academic.GET("/assignments", routes.ListAssignmentsHandler(log))
	academic.POST("/assignments", routes.CreateAssignmentHandler(log))
	academic.GET("/assignments/:id/submissions", routes.ListSubmissionsHandler(log))
	academic.POST("/assignments/:id/submit", routes.SubmitAssignmentHandler(log))
	academic.GET("/grades", routes.ListGradesHandler(log))
	academic.POST("/grades", routes.CreateGradeHandler(log))
	academic.GET("/attendance", routes.ListAttendanceHandler(log))
	academic.POST("/attendance", routes.MarkAttendanceHandler(log))

	// Finance routes
	finance := protected.Group("/finance")
	finance.GET("/fee-structures", routes.ListFeeStructuresHandler(log))
	finance.POST("/fee-structures", routes.CreateFeeStructureHandler(log))
	finance.GET("/student-fees", routes.ListStudentFeesHandler(log))
	finance.GET("/invoices", routes.ListInvoicesHandler(log))
	finance.POST("/invoices", routes.CreateInvoiceHandler(log))
	finance.GET("/payments", routes.ListPaymentsHandler(log))
	finance.POST("/payments", routes.CreatePaymentHandler(log))
	finance.GET("/expenses", routes.ListExpensesHandler(log))
	finance.POST("/expenses", routes.CreateExpenseHandler(log))
	finance.GET("/payroll", routes.ListPayrollRunsHandler(log))

	// HR routes
	hr := protected.Group("/hr")
	hr.GET("/staff", routes.ListStaffHandler(log))
	hr.POST("/staff", routes.CreateStaffHandler(log))
	hr.GET("/leave-types", routes.ListLeaveTypesHandler(log))
	hr.POST("/leave-types", routes.CreateLeaveTypeHandler(log))
	hr.GET("/leave-balances", routes.ListLeaveBalancesHandler(log))
	hr.GET("/leave-requests", routes.ListLeaveRequestsHandler(log))
	hr.POST("/leave-requests", routes.CreateLeaveRequestHandler(log))
	hr.PUT("/leave-requests/:id/approve", routes.ApproveLeaveRequestHandler(log))
	hr.PUT("/leave-requests/:id/reject", routes.RejectLeaveRequestHandler(log))
	hr.GET("/contracts", routes.ListContractsHandler(log))
	hr.POST("/contracts", routes.CreateContractHandler(log))
	hr.GET("/performance", routes.ListPerformanceReviewsHandler(log))

	// Communication routes
	comm := protected.Group("/communication")
	comm.GET("/announcements", routes.ListAnnouncementsHandler(log))
	comm.POST("/announcements", routes.CreateAnnouncementHandler(log))
	comm.GET("/conversations", routes.ListConversationsHandler(log))
	comm.POST("/conversations", routes.CreateConversationHandler(log))
	comm.GET("/conversations/:id/messages", routes.ListMessagesHandler(log))
	comm.POST("/conversations/:id/messages", routes.SendMessageHandler(log))
	comm.GET("/notifications", routes.ListNotificationsHandler(log))
	comm.PUT("/notifications/:id/read", routes.MarkNotificationReadHandler(log))
	comm.GET("/calendar", routes.ListCalendarEventsHandler(log))
	comm.POST("/calendar", routes.CreateCalendarEventHandler(log))

	// Analytics routes
	analytics := protected.Group("/analytics")
	analytics.GET("/dashboards", routes.ListDashboardsHandler(log))
	analytics.GET("/dashboards/:id", routes.GetDashboardHandler(log))
	analytics.GET("/reports", routes.ListReportsHandler(log))
	analytics.POST("/reports", routes.CreateReportHandler(log))
	analytics.POST("/reports/:id/run", routes.RunReportHandler(log))
	analytics.GET("/insights", routes.ListInsightsHandler(log))
	analytics.GET("/kpis", routes.ListKPIsHandler(log))

	// WebSocket endpoint
	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}
	e.GET("/hubs/notifications", func(c echo.Context) error {
		userID := tenant.GetUserID(c)
		instituteID := tenant.GetTenantID(c)
		userRole := tenant.GetUserRole(c)
		claims := tenant.GetClaims(c)

		var roles []string
		if claims != nil {
			roles = claims.Roles
		}
		if userRole != "" {
			roles = append(roles, userRole)
		}

		ws, err := upgrader.Upgrade(c.Response(), c.Request(), nil)
		if err != nil {
			log.Error().Err(err).Msg("Failed to upgrade WebSocket")
			return err
		}
		
		wsHub.RegisterClient(ws, userID, instituteID, roles)
		return nil
	})

	// Start server
	go func() {
		addr := fmt.Sprintf("%s:%d", cfg.App.Host, cfg.App.Port)
		log.Info().Str("addr", addr).Msg("Starting HTTP server")
		if err := e.Start(addr); err != nil && err != http.ErrServerClosed {
			log.Fatal().Err(err).Msg("Server failed")
		}
	}()

	// Wait for interrupt signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Info().Msg("Shutting down server...")

	// Graceful shutdown
	_, cancel = context.WithTimeout(context.Background(), cfg.App.ShutdownGracePeriod)
	defer cancel()

	if err := e.Shutdown(ctx); err != nil {
		log.Fatal().Err(err).Msg("Server forced to shutdown")
}

log.Info().Msg("Server exited")
}

type redisWrapper struct {
	client *redis.Client
}

func (w *redisWrapper) Set(ctx context.Context, key string, value interface{}, expiration time.Duration) error {
	return w.client.Set(ctx, key, value, expiration).Err()
}

func (w *redisWrapper) Get(ctx context.Context, key string) (string, error) {
	return w.client.Get(ctx, key).Result()
}

func (w *redisWrapper) Del(ctx context.Context, keys ...string) error {
	return w.client.Del(ctx, keys...).Err()
}

func (w *redisWrapper) Exists(ctx context.Context, keys ...string) (int64, error) {
	return w.client.Exists(ctx, keys...).Result()
}

func (w *redisWrapper) Expire(ctx context.Context, key string, expiration time.Duration) error {
	return w.client.Expire(ctx, key, expiration).Err()
}

func (w *redisWrapper) TTL(ctx context.Context, key string) (time.Duration, error) {
	return w.client.TTL(ctx, key).Result()
}

func (w *redisWrapper) Keys(ctx context.Context, pattern string) ([]string, error) {
	return w.client.Keys(ctx, pattern).Result()
}

func (w *redisWrapper) Close() error {
	return w.client.Close()
}

var startTime = time.Now()