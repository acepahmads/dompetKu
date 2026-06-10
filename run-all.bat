@echo off
title DompetKu Launcher
color 0b

echo ===================================================
echo             DOMPETKU - LAUNCHER SYSTEMS
echo ===================================================
echo.

echo [1/2] Starting DompetKu Go Backend (Port 8085)...
start "DompetKu Backend API" cmd /k "go run ."

echo [2/2] Starting DompetKu Vue Frontend (Port 5173)...
start "DompetKu Frontend Dev" cmd /k "npm run dev"

echo.
echo ===================================================
echo [SUCCESS] Both servers launched in separate windows!
echo.
echo * Backend API:   http://localhost:8085
echo * Frontend App:  http://localhost:5173
echo.
echo (You can close this launcher window. The servers will
echo  continue running in their own windows.)
echo ===================================================
echo.
pause
