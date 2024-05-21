use std::fs::File;
use std::collections::HashMap;
use std::io::{Write,Result};

use crate::Topic;

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