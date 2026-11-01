import puppeteer from 'puppeteer';

const BASE = 'http://127.0.0.1:8090';

async function run() {
  const browser = await puppeteer.launch({
    headless: true,
    args: ['--no-sandbox', '--disable-setuid-sandbox'],
  });
  
  const page = await browser.newPage();
  
  // Test 1: Main page loads
  console.log('Test 1: Main page loads...');
  await page.goto(BASE);
  const title = await page.title();
  if (!title.includes('AI-штурм')) {
    throw new Error(`Title mismatch: got "${title}", expected "AI-штурм"`);
  }
  console.log('✅ Main page loads');
  
  // Test 2: Auth endpoint works
  console.log('Test 2: Auth endpoint...');
  const authRes = await page.evaluate(async () => {
    const res = await fetch('/api/auth');
    return res.json();
  });
  if (!authRes.ok) {
    throw new Error(`Auth failed: ${JSON.stringify(authRes)}`);
  }
  console.log('✅ Auth endpoint works');
  
  // Test 3: Projects list loads
  console.log('Test 3: Projects list...');
  const projects = await page.evaluate(async () => {
    const res = await fetch('/api/projects');
    return res.json();
  });
  if (!Array.isArray(projects) || projects.length === 0) {
    throw new Error(`Projects not loaded: ${JSON.stringify(projects)}`);
  }
  console.log(`✅ Projects list loaded (${projects.length} projects)`);
  
  // Test 4: Project detail loads
  console.log('Test 4: Project detail...');
  const detail = await page.evaluate(async () => {
    const res = await fetch('/api/projects/ollama');
    return res.json();
  });
  if (!detail.epics || !detail.tasks) {
    throw new Error(`Project detail malformed: ${JSON.stringify(detail)}`);
  }
  console.log('✅ Project detail loads');
  
  // Test 5: Chat endpoint works
  console.log('Test 5: Chat endpoint...');
  const chat = await page.evaluate(async () => {
    const res = await fetch('/api/projects/ollama/chat');
    return res.json();
  });
  if (!Array.isArray(chat)) {
    throw new Error(`Chat not loaded: ${JSON.stringify(chat)}`);
  }
  console.log('✅ Chat endpoint works');
  
  console.log('\n✅ All tests passed!');
  await browser.close();
}

run().catch(err => {
  console.error('❌ Test failed:', err.message);
  process.exit(1);
});
