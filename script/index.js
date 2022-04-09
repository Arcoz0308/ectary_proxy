const express = require("express");
const app = express();

app.get('/', function (req, res) {
    res.send('3');
})
app.get('/1', function (req, res) {
    res.send('5');
})
app.get('/2', function (req, res) {
    res.send('34');
})
app.get('/3', function (req, res) {
    res.send('8');
})
app.listen(9001);