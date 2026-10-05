const http=require("http"),fs=require("fs"),path=require("path");
const ROOT="/www";
http.createServer((req,res)=>{
  let p=path.join(ROOT,req.url.split("?")[0]);
  if(p.endsWith("/"))p+="index.html";
  fs.readFile(p,(e,d)=>{
    if(e){res.writeHead(404);res.end("not found");return}
    const ct=p.endsWith(".html")?"text/html; charset=utf-8":p.endsWith(".md")?"text/plain; charset=utf-8":"application/octet-stream";
    res.writeHead(200,{"Content-Type":ct});res.end(d);
  });
}).listen(80,()=>console.log("hub up"));
