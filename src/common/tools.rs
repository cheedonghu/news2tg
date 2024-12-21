use std::fs::File;
use std::io::{Write,Result,Read};
use std::collections::HashMap;
use std::fs::OpenOptions;
use crate::common::models::Topic;


/// 以utf8格式进行字符分割
pub fn truncate_utf8(s: &str, max_chars: usize) -> String {
    s.chars().take(max_chars).collect()
}

pub fn escape_markdown_v2(text: &str) -> String {
    // Define the characters that need to be escaped in Markdown V2
    let escape_chars = ['_', '*', '[', ']', '(', ')', '~', '`', '>', '#', '+', '-', '=', '|', '{', '}', '.', '!'];
    let mut escaped_text = String::new();

    // Iterate over each character in the input text
    for c in text.chars() {
        // If the character is in the list of escape_chars, prepend a backslash
        if escape_chars.contains(&c) {
            escaped_text.push('\\');
        }
        escaped_text.push(c);
    }

    escaped_text
}


pub fn write_content_to_file(section_title: &str, content: &str,append: bool) -> Result<()> {
    let file = if append {
        OpenOptions::new().append(true).open("output.txt")?
    } else {
        File::create("output.txt")?
    };

    // 使用缓冲
    let mut file = std::io::BufWriter::new(file);
    file.flush()?;
    writeln!(file, "{}:", section_title)?;
    writeln!(file, "{}\n", content)?;
    Ok(())
}

pub fn read_content_from_file() -> Result<String> {
    let mut file = File::open("output.txt")?;  // 打开文件
    let mut content = String::new();           // 创建一个空的 String
    file.read_to_string(&mut content)?;        // 将文件内容读入到 String 中
    Ok(content)                                // 返回内容
}


pub fn write_topics_to_file(section_title: &str, topics: &[Topic], pushed_urls: &HashMap<String, String>) -> Result<()> {
    let mut file = File::create("output.txt")?;
    writeln!(file, "{}:", section_title)?;
    for topic in topics {
        if !pushed_urls.contains_key(&topic.url){
            writeln!(file, "ID: {}", topic.id)?;
            writeln!(file, "Title: {}", topic.title)?;
            writeln!(file, "URL: {}", topic.url)?;
            if let Some(content) = &topic.content {
                writeln!(file, "Content: {}", content)?;
            }
            writeln!(file, "Replies: {}", topic.replies)?;
            writeln!(file, "Member: {} (ID: {})", topic.member.username, topic.member.id)?;
            writeln!(file, "Node: {} (ID: {})", topic.node.title, topic.node.id)?;
            writeln!(file)?;
        }
    }
    Ok(())
}

// pub fn truncate_html_old(string: &str) -> Result<String> {
//     // Read the HTML file content
//     // let mut file = File::open("./output.txt")?;
//     let mut content = String::from(string);
//     // file.read_to_string(&mut content)?;

//     // Remove HTML comments
//     let re_comments = Regex::new(r"(?s)<!--.*?-->").unwrap();
//     content = re_comments.replace_all(&content, "").to_string();

//     // Minify inline CSS
//     let re_css = Regex::new(r"<style>(.*?)</style>").unwrap();
//     content = re_css.replace_all(&content, |caps: &regex::Captures| {
//         match css::minify(&caps[1]) {
//             Ok(minified_css) => format!("<style>{}</style>", minified_css),
//             Err(e) => {
//                 eprintln!("Error minifying CSS: {}", e);
//                 format!("<style>{}</style>", &caps[1]) // Return original CSS on error
//             }
//         }
//     }).to_string();

//     // Minify inline JavaScript
//     let re_js = Regex::new(r"<script>(.*?)</script>").unwrap();
//     content = re_js.replace_all(&content, |caps: &regex::Captures| {
//         format!("<script>{}</script>", js::minify(&caps[1].to_string()))
//     }).to_string();

//     // Minify HTML (remove redundant whitespaces between tags)
//     let re_html = Regex::new(r">\s+<").unwrap();
//     content = re_html.replace_all(&content, "><").to_string();

//     // Save the modified content back to a file
//     if cfg!(debug_assertions) {
//         let mut output = File::create("output.html")?;
//         output.write_all(&content.as_bytes())?;
//     }
//     // let mut output = File::create("output.html")?;
//     // output.write_all(content.as_bytes())?;

//     Ok(content)
// }

// /// 优化html内容减少token数
// pub fn truncate_html(string: &str) -> Result<String> {
//     // Read the HTML file content
//     // let mut file = File::open("./output.txt")?;
//     let mut content = String::from(string);
//     if cfg!(debug_assertions) {
//         let mut output = File::create("output.txt")?;
//         output.write_all(&content.as_bytes())?;
//     }
//     // let mut content=String::new();
//     // file.read_to_string(&mut content);

//     // Remove HTML comments
//     let re_comments = Regex::new(r"(?s)<!--.*?-->").unwrap();
//     content = re_comments.replace_all(&content, "").to_string();

//     // Minify inline CSS
//     let re_css = Regex::new(r"<style>(.*?)</style>").unwrap();
//     content = re_css.replace_all(&content, |caps: &regex::Captures| {
//         match css::minify(&caps[1]) {
//             Ok(minified_css) => format!("<style>{}</style>", minified_css),
//             Err(e) => {
//                 eprintln!("Error minifying CSS: {}", e);
//                 format!("<style>{}</style>", &caps[1]) // Return original CSS on error
//             }
//         }
//     }).to_string();

//     // Minify inline JavaScript
//     let re_js = Regex::new(r"<script>(.*?)</script>").unwrap();
//     content = re_js.replace_all(&content, |caps: &regex::Captures| {
//         format!("<script>{}</script>", js::minify(&caps[1].to_string()))
//     }).to_string();

//     // Minify HTML (remove redundant whitespaces between tags)
//     let re_html = Regex::new(r">\s+<").unwrap();
//     content = re_html.replace_all(&content, "><").to_string();

//     // Save the modified content back to a file
//     if cfg!(debug_assertions) {
//         let mut output = File::create("output.html")?;
//         output.write_all(&content.as_bytes())?;
//     }
//     // let mut output = File::create("output.html")?;
//     // output.write_all(content.as_bytes())?;

//     // 创建选择器以选取文档中的所有文本节点
//     let selector = Selector::parse("*").unwrap();
//     let mut text_content = String::new();
//     let document = Html::parse_document(&content);


//     // 遍历每个元素，提取其文本
//     for element in document.select(&selector) {
//         if let Some(text) = element.text().next() {
//             let cleaned_text = text.split_whitespace().collect::<Vec<_>>().join(" ");
//             if !cleaned_text.is_empty() {
//                 text_content.push_str(&cleaned_text);
//                 text_content.push(' ');  // 添加空格以保持单词间隔
//             }
//         }
//     }

//     // 输出或保存提取后的文本
//     if cfg!(debug_assertions) {
//         let mut output = File::create("output.html")?;
//         output.write_all(&text_content.as_bytes())?;
//     }

//     Ok(text_content)
// }


// pub fn tokenize(s: &str) -> Vec<String> {
//     let ascii_patt = Regex::new(r"([\x00-\xFF]+)").unwrap();
//     let mut tokens = Vec::new();
//     for token in ascii_patt.split(s.trim()) {
//         if !token.is_empty() {
//             if ascii_patt.is_match(token) {
//                 tokens.extend(token.split_whitespace().map(|t| format!("{} ", t)));
//             } else {
//                 tokens.extend(token.chars().map(|c| c.to_string()));
//             }
//         }
//     }
//     tokens
// }

// // #[cached(size = 32)]
// pub fn cached_tokenize(s: &str) -> Vec<String> {
//     tokenize(s)
// }

// #[cached(size = 128)]
// pub fn lcs_length(x: Vec<String>, y: Vec<String>) -> usize {
//     let (len_x, len_y) = (x.len() + 1, y.len() + 1);
//     let mut lcs = vec![vec![0; len_y]; len_x];
//     for i in 1..len_x {
//         for j in 1..len_y {
//             lcs[i][j] = if x[i - 1] == y[j - 1] {
//                 lcs[i - 1][j - 1] + 1
//             } else {
//                 usize::max(lcs[i - 1][j], lcs[i][j - 1])
//             };
//         }
//     }
//     lcs[len_x - 1][len_y - 1]
// }

// #[cached(size = 128)]
// pub fn string_inclusion_ratio(needle: String, haystack: String) -> f64 {
//     let needle_tokens = cached_tokenize(&needle);
//     let haystack_tokens = cached_tokenize(&haystack);
//     if needle_tokens.is_empty() || haystack_tokens.is_empty() {
//         return 0.0;
//     }
//     lcs_length(needle_tokens, haystack_tokens) as f64 / cached_tokenize(&needle).len() as f64
// }



#[cfg(test)]
mod tests{

    // use super::*;
    use std::io::Result;
    // use std::fs::File;



    // #[test]
    // fn test_truncate_html() -> Result<()>{
    //     truncate_html("123");
    //     Ok(())
    // }
}
