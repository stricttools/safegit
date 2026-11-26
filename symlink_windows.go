package main

import (
	"encoding/base64"
	"encoding/binary"
	"strings"
	"unicode/utf16"
)

// Record the native kernel operation through Effects.Run. Git's MSYS ln may
// copy a target instead of creating a symlink, which silently changes its type.
// Encode the script, and single-quote/escape data; no path becomes shell code.
func symlinkCommand(target, path string) []interface{} {
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	script := `$ErrorActionPreference='Stop'; $ProgressPreference='SilentlyContinue'; $env:LIB=$null; $env:LIBPATH=$null
Add-Type -TypeDefinition 'using System; using System.Runtime.InteropServices; public static class SafegitNativeLink { [DllImport("kernel32.dll", CharSet=CharSet.Unicode, SetLastError=true)] [return: MarshalAs(UnmanagedType.I1)] public static extern bool CreateSymbolicLink(string path,string target,int flags); }'
$p=` + quote(path) + `; $t=` + quote(target) + `
$r=$t; if (-not [IO.Path]::IsPathRooted($r)) { $r=[IO.Path]::Combine([IO.Path]::GetDirectoryName($p),$r) }
$f=2; if ([IO.Directory]::Exists($r)) { $f=$f -bor 1 }
$ok=[SafegitNativeLink]::CreateSymbolicLink($p,$t,$f)
if (-not $ok) { $e=[Runtime.InteropServices.Marshal]::GetLastWin32Error(); if ($e -eq 87) { $ok=[SafegitNativeLink]::CreateSymbolicLink($p,$t,($f -band 1)); $e=[Runtime.InteropServices.Marshal]::GetLastWin32Error() }; if (-not $ok) { throw (New-Object ComponentModel.Win32Exception $e) } }
`
	units := utf16.Encode([]rune(script))
	bytes := make([]byte, len(units)*2)
	for i, u := range units {
		binary.LittleEndian.PutUint16(bytes[2*i:], u)
	}
	return []interface{}{"powershell.exe", "-NoProfile", "-NonInteractive", "-EncodedCommand", base64.StdEncoding.EncodeToString(bytes)}
}
