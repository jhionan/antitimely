#!/usr/bin/env python3
# Logs raw bytes the terminal sends to stdin (in cbreak+altscreen, like `atl status`),
# with timestamps, so we can see what fires every ~15s. Ctrl-C to stop.
import sys, termios, tty, time, select, os
fd = sys.stdin.fileno()
old = termios.tcgetattr(fd)
sys.stdout.write("\x1b[?1047h\x1b[H\x1b[J")          # alt-screen, like the status view
sys.stdout.write("capturing raw stdin ~ leave it alone; Ctrl-C to stop\r\n")
sys.stdout.flush()
try:
    tty.setcbreak(fd)                                 # ICANON/ECHO off, ISIG kept
    t0 = time.time()
    while True:
        if select.select([fd], [], [], 1)[0]:
            data = os.read(fd, 64)
            if not data: break
            hexs = " ".join(f"{b:02x}" for b in data)
            rep  = data.decode("latin1").replace("\x1b", "<ESC>")
            sys.stdout.write(f"[{time.time()-t0:6.1f}s] n={len(data):2d}  {hexs}   {rep!r}\r\n")
            sys.stdout.flush()
            if data == b"\x03": break                 # Ctrl-C
except KeyboardInterrupt:
    pass
finally:
    termios.tcsetattr(fd, termios.TCSADRAIN, old)
    sys.stdout.write("\x1b[?1047l")                   # leave alt-screen
    sys.stdout.flush()
