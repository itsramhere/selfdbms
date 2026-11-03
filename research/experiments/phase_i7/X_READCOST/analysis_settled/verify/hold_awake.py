"""Hold a per-process power request while xcampaign runs. Changes no setting; ends with xcampaign, at most 8 h."""
import ctypes, subprocess, time

ES_CONTINUOUS, ES_SYSTEM_REQUIRED = 0x80000000, 0x00000001
ctypes.windll.kernel32.SetThreadExecutionState(ES_CONTINUOUS | ES_SYSTEM_REQUIRED)
start = time.time()
while time.time() - start < 8 * 3600:
    time.sleep(300)
    out = subprocess.run(["tasklist", "/FI", "IMAGENAME eq xcampaign.exe", "/NH"], capture_output=True, text=True).stdout
    if "xcampaign.exe" not in out:
        break
print("released after", round((time.time() - start) / 3600, 2), "h")
