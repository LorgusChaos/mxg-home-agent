@echo off
rem Opens the inverter data viewer (http://localhost:8787). Data is copied from the box over adb into data\.
rem The box address (IP:5555) is taken from MXG_BOX or from box.txt next to this file (see box.txt.example).
cd /d "%~dp0"
if "%MXG_BOX%"=="" if exist box.txt set /p MXG_BOX=<box.txt
start "mxgview" /min "%~dp0agent\build\mxgview.exe"
