// extern crate regex;
// extern crate scraper;

// use kuchikiki::{ElementData,Node,Selectors,NodeData};
// use regex::Regex;
// use kuchikiki::traits::*;
// use kuchikiki::NodeRef;
// use std::borrow::BorrowMut;
// use std::collections::{HashSet, HashMap};
// use std::ops::DerefMut;
// use url::Url;
// use std::num::NonZeroUsize;
// use lazy_static::lazy_static;
// use lru::LruCache;
// use std::sync::Mutex;


// use crate::tools::{string_inclusion_ratio, tokenize};

// 尝试重写https://github.com/polyrabbit/hacker-news-digest/tree/master中摘要代码，但因为无与bs4兼容度高的库失败
// 后续尝试自己实现

// lazy_static! {
//     static ref IGNORED_TAGS: HashSet<&'static str> = {
//         let mut m = HashSet::new();
//         m.insert("option");
//         m.insert("script");
//         m.insert("noscript");
//         m.insert("style");
//         m.insert("iframe");
//         m.insert("head");
//         m
//     };
//     static ref BLOCK_TAGS: HashSet<&'static str> = {
//         let mut m = HashSet::new();
//         m.insert("article");
//         m.insert("header");
//         m.insert("aside");
//         m.insert("hgroup");
//         m.insert("blockquote");
//         m.insert("hr");
//         m.insert("body");
//         m.insert("li");
//         m.insert("br");
//         m.insert("map");
//         m.insert("button");
//         m.insert("object");
//         m.insert("canvas");
//         m.insert("ol");
//         m.insert("caption");
//         m.insert("output");
//         m.insert("col");
//         m.insert("p");
//         m.insert("colgroup");
//         m.insert("pre");
//         m.insert("dd");
//         m.insert("progress");
//         m.insert("div");
//         m.insert("section");
//         m.insert("dl");
//         m.insert("table");
//         m.insert("dt");
//         m.insert("tbody");
//         m.insert("embed");
//         m.insert("textarea");
//         m.insert("fieldset");
//         m.insert("tfoot");
//         m.insert("figcaption");
//         m.insert("th");
//         m.insert("figure");
//         m.insert("thead");
//         m.insert("footer");
//         m.insert("tr");
//         m.insert("form");
//         m.insert("ul");
//         m.insert("h1");
//         m.insert("h2");
//         m.insert("h3");
//         m.insert("h4");
//         m.insert("h5");
//         m.insert("h6");
//         m.insert("video");
//         m.insert("td");
//         m
//     };
//     static ref NEGATIVE_PATTERNS: Regex = Regex::new(r"comment|combx|disqus|foot|header|menu|rss|button|hidden|collaps|toggle|shoutbox|sidebar|sponsor|vote|meta|shar|ad-").unwrap();
//     static ref POSITIVE_PATTERNS: Regex = Regex::new(r"article|entry|post|abstract|main|content|toptext|section|text|preview|view|story-body").unwrap();
// }

// pub struct HtmlContentExtractor {
//     url: String,
//     pub doc: NodeRef,
//     title: String,
//     article: Option<String>,
//     max_score: i32,
//     scores: HashMap<String, i32>,
//     img_area_cache: Mutex<LruCache<String, usize>>,
//     pub meta_description: String
// }

// impl HtmlContentExtractor {
//     pub fn new(html: &str, url: &str) -> Self {
//         let doc: NodeRef=kuchikiki::parse_html().one(html);
//         let title = HtmlContentExtractor::find_title(&doc);
        
//         let mut extractor = HtmlContentExtractor {
//             url: url.to_string(),
//             doc,
//             title,
//             article: None,
//             max_score: -1,
//             scores: HashMap::new(),
//             img_area_cache: Mutex::new(LruCache::new(NonZeroUsize::new(1024).unwrap())),
//             meta_description: String::new()
//         };
        
//         // extractor.get_favicon_url();
//         extractor.meta_description=extractor.get_meta_description_2();
//         // extractor.get_meta_image();
//         extractor.purge();
//         extractor.find_main_content();
//         // extractor.relative_path2_abs_url();
        
//         extractor
//     }

//     // fn get_article(doc: &Html) -> Option<String> {
//     //     let selector = Selector::parse("body").unwrap();
//     //     let body = doc.select(&selector).next()?;
//     //     let mut text = String::new();
//     //     for node in body.text() {
//     //         if !IGNORED_TAGS.contains(node) && !NEGATIVE_PATTERNS.is_match(node) {
//     //             text.push_str(node);
//     //         }
//     //     }
//     //     if text.len() > 100 { // Arbitrary threshold for article length
//     //         Some(text)
//     //     } else {
//     //         None
//     //     }
//     // }

//     fn calc_img_area_len(&self, src: &str) -> usize {
//         let mut cache = self.img_area_cache.lock().unwrap();
//         if let Some(&area) = cache.get(src) {
//             return area;
//         }
//         // Dummy image area calculation logic
//         let area = 100; // Example value
//         cache.put(src.to_string(), area);
//         area
//     }

//     pub fn get_meta_description(&self) -> String {
//         // <meta name="twitter:description" content="..."/>
//         // <meta property="og:description" content="..."/>
//         // let selector = Selectors::compile(r#"meta[name*="description"],meta[property*="description"]"#).unwrap();
//         // let descs: Vec<_> = self.doc.select(&selector).collect();

//         let descs: Vec<_> = self.doc.select(r#"meta[name*="description"],meta[property*="description"]"#).unwrap().collect();

//         let mut result=String::new();
//         for desc in descs{
//             if let Some(content)=desc.attributes.borrow().get("content"){
//                 if content.len()>result.len(){
//                     result=String::from(content);
//                 }
//             }
//         }
//         result
//     }

//     pub fn get_meta_description_2(&self) -> String {
//         // <meta name="twitter:description" content="..."/>
//         // <meta property="og:description" content="..."/>
//         let select_result=self.doc.select(r#"meta[name*="description"],meta[property*="description"]"#);
//         // let selector = Selector::parse(r#"meta[name*="description"],meta[property*="description"]"#).unwrap();
//         // let descs: Vec<_> = self.doc.select(&selector).collect();

//         let mut description=String::new();
//         if let Ok(result)=select_result{
//             for element in result{
//                 let attributes=element.attributes.borrow();
//                 if let Some(content)=attributes.get("content"){
//                     if content.len()>description.len(){
//                         description=String::from(content);
//                     }
//                 }
//             }

//         }
        
//         description
//     }

//     pub fn get_summary(&self, max_length: usize) -> String {
//         let article = match &self.article {
//             Some(article) => article,
//             None => return String::new(),
//         };

//         if article.len() <= max_length {
//             return article.clone();
//         }

//         let mut summary = String::new();
//         for word in tokenize(article) {
//             if summary.len() + word.len() > max_length {
//                 break;
//             }
//             summary.push_str(&word);
//             summary.push(' ');
//         }

//         summary.trim().to_string()
//     }

//     fn purge(&self) {
//         // Implement purging logic
        
//         // remove ignored tag
//         for ignore_tag in IGNORED_TAGS.iter(){
//             let select_result=self.doc.select(*ignore_tag);
//             if let Ok(elements) = select_result {
//                 let to_remove:Vec<NodeRef>=elements.map(|x| x.as_node().clone()).collect();

//                 for node in to_remove {
//                     node.detach();
//                 }
//             }
//         }

//         // todo remove hidden style

//         // remove style links
//     }

//     fn find_title(doc:&NodeRef) -> String{
//         let title = String::new();
//         if let Some(element)=doc.as_element(){
//             let attr = element.attributes.borrow();
//             return attr.get("title").unwrap_or("").to_string()
//         }
//         title
//     }

//     // fn find_main_content_2(&mut self) {
//     //     let selector = Selector::parse("body").unwrap();
//     //     if let Some(body) = self.doc.select(&selector).next() {
//     //         let mut max_score = 0;
//     //         let mut main_content = None;
//     //         for element in body.descendants() {
//     //             if let Some(tag) = element.value().as_element() {
//     //                 let text_len = self.calc_effective_text_len(&tag.html());
//     //                 if text_len > max_score {
//     //                     max_score = text_len;
//     //                     main_content = Some(tag.html());
//     //                 }
//     //             }
//     //         }
//     //         self.article = main_content;
//     //     }
//     // }

//     // pub fn as_element(node: &Node) -> Option<ElementData> {
//     //     match node.data() {
//     //         NodeData::Element(value) => Some(value),
//     //         _ => None,
//     //     }
//     // }


//     fn find_main_content(&mut self){
//         self.calc_effective_text_len(&self.doc, 1.00);
//         // self.set_title_parents_point(self);
//         // self.set_article_tag_point(self);
//         // self.calc_node_score(self);
//         // logger.info(f'Max score: {self.article.score or 0:.2f}, node: {" ".join(self.node_identify(self.article))}')
//     }


//     /// Calc the total the length of text in a child, same as
//     /// sum(len(s) for s in cur_node.stripped_strings)
//     pub fn calc_effective_text_len(&self, node: &NodeRef, negative_factor: f64) -> String {
//         let mut negative_factor=negative_factor;
        
//         // let negative_factor: f64=1.00;
//         let mut text_len: f64=0.00;
//         if let Some(document)=node.as_document(){
//             println!("document");
//         }else if let Some(doc_type)=node.as_doctype(){
//             println!("{}",doc_type.name);
//         }else if let Some(element) = node.as_element(){
//             let attr = element.attributes.borrow();
//             if attr.contains("text_len"){
//                 return attr.get("text_len").unwrap().to_string()
//             }
//             if self.has_negative_effect(element) || "a".eq(&element.name.local.to_string()){
//                 negative_factor*=0.2;
//             }
//             if negative_factor!=1.00{
//                 self.set_node_factor(element, "negative", negative_factor);
//             }
//         }

//         for child in node.children(){
//             if child.as_element().is_some(){
//                 let child_len: f64=self.calc_effective_text_len(&child,negative_factor).parse().unwrap();
//                 text_len+= child_len / negative_factor;
//             }else if let Some(text)=child.as_text(){
//                 // text_len += len(child.string.strip()) + child.string.count(',') + \
//                 //             child.string.count('，')  # Chinese comma
//                 let string = text.borrow();
//                 let comma_1: f64=string.chars().filter(|&c| c == ',').count() as f64;
//                 let comma_2: f64=string.chars().filter(|&c| c == '，').count() as f64;
//                 text_len+=string.trim().len() as f64 + comma_1/comma_2;
//             }
//         }

//         // todo as_element一定会有吗？ 自定义属性放哪呢？ 单独开个map来映射吗
//         if let Some(element) = node.as_element(){
//             let mut attr = element.attributes.borrow_mut();
//             attr.insert("real_text_len", text_len.to_string());
//             attr.insert("text_len", (text_len * negative_factor).to_string());
//         }

//         return (text_len * negative_factor).to_string()
//     }

//     fn set_node_factor(&self, element: &ElementData, factor: &str, value: f64){
//         // if not node.impact_factor:
//         // node.impact_factor = {}
//         // node.impact_factor[factor] = value
//         let mut attr = element.attributes.borrow_mut();
//         attr.insert(factor, value.to_string());
//     }
    
//     fn has_negative_effect(&self, element: &ElementData) -> bool {
//         for attr in self.node_identify(element){
//             if attr.len()>0 && NEGATIVE_PATTERNS.is_match(&attr){
//                 return true
//             }
//         }
//         false
//     }

//     fn node_identify(&self, element: &ElementData) -> Vec<String>{
        
//         let mut identifiers:Vec<String> = Vec::new();
//         identifiers.push(element.name.local.trim().to_string());
//         let attr = element.attributes.borrow();
//         if let Some(class) = attr.get("class"){
//             // todo 这里的内容可能需要按照空格分割？
//             identifiers.push(class.to_string());
//         }
//         if let Some(id) = attr.get("id"){
//             // todo 这里的内容可能需要按照空格分割？
//             identifiers.push(id.to_string());
//         }
//         identifiers
//     }

// }




// #[cfg(test)]
// mod tests{
//     extern crate regex;
//     extern crate scraper;
//     use crate::html::*;
//     use kuchikiki::traits::*;
//     use kuchikiki::NodeRef;



//     #[test]
//     fn test_get_meta_des(){

//         let html=r#"
//             <!DOCTYPE html>
//             <html lang="en">
//             <head>
//                 <meta charset="UTF-8">
//                 <meta name="viewport" content="width=device-width, initial-scale=1.0">
//                 <title>Document</title>
//                 <meta property="og:description" content="aaaa" />
//                 <meta name="twitter:description" content="bbbBB" />
//                 <meta name="description" content="bbb" />
//                 <meta name="twitter:description" content="bbbBB" />
//             </head>
//             <body>
                
//             </body>
//             </html>
//                     "#;

//         // let doc = Html::parse_document(html);
//         let doc=kuchikiki::parse_html().one(html);

//         // 初始化
//         let extractor=HtmlContentExtractor::new(html,"123");

//         println!("{}",extractor.meta_description);
//         println!("test");

//     }

//     #[test]
//     fn test_purge(){

//         let html=r#"
//             <!DOCTYPE html>
//             <html lang="en">
//             <head>
//             </head>
//             <body>
//                 good<script>whatever</script>
//             </body>
//             </html>
//                     "#;

//         // let doc = Html::parse_document(html);
//         // let doc=kuchikiki::parse_html().one(html);

//         // 初始化
//         let extractor=HtmlContentExtractor::new(html,"123");

//         assert_eq!("good",extractor.doc.text_contents().trim());
//         println!("test");

//     }

//     #[test]
//     fn test_text_len_with_comma(){
//     let html=r#"
//              <html>good,，</html>
//              "#;
//     let extractor=HtmlContentExtractor::new(html,"123");
//     let length = extractor.calc_effective_text_len(&extractor.doc, 1.00);
//     println!("{}",&length);
//     assert_eq!(length, "8".to_string())
//     }
// }


