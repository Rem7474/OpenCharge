from playwright.sync_api import sync_playwright

with sync_playwright() as p:
    browser = p.chromium.launch(headless=False)
    page = browser.new_page()
    page.goto("https://www.tesla.com/api/findus/get-locations", wait_until="networkidle")
    text = page.evaluate("document.body.innerText")
with open("page.json", "w", encoding="utf-8") as f:
    f.write(text)
    browser.close()