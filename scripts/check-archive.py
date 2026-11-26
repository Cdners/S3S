"""Verify the package a user will extract from the Actions download."""

import sys
import tarfile
import zipfile


def check(platform, path):
    if platform == "windows":
        with zipfile.ZipFile(path) as package:
            if package.namelist() != ["S3S.exe"]:
                raise ValueError("Windows ZIP must contain only S3S.exe at the root")
            with package.open("S3S.exe") as executable:
                if executable.read(2) != b"MZ" or package.testzip() is not None:
                    raise ValueError("Invalid Windows executable or ZIP checksum")
        return

    with tarfile.open(path, "r:gz") as package:
        entries = package.getnames()
        if platform == "linux":
            if entries != ["S3S"]:
                raise ValueError("Linux archive must contain only S3S at the root")
            binary = "S3S"
        elif platform == "darwin":
            if any(p != "S3S.app" and not p.startswith("S3S.app/") for p in entries):
                raise ValueError("macOS archive must contain one S3S.app bundle")
            package.getmember("S3S.app/Contents/Info.plist")
            binary = "S3S.app/Contents/MacOS/S3S"
        else:
            raise ValueError(f"Unsupported platform {platform}")
        member = package.getmember(binary)
        if not member.isfile() or not member.mode & 0o111 or member.size == 0:
            raise ValueError(f"Missing executable permissions or contents for {binary}")


if __name__ == "__main__":
    check(*sys.argv[1:])
